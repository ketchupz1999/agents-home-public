#!/usr/bin/env python3
"""aspace-poller — lightweight daemon that polls for wakeup tasks and auto-replies.

Usage:
  python3 aspace-poller.py --profile research-agent [--backend hermes|anthropic|cmd] [--once]

Reads server+key from $ASPACE_HOME/profiles/<id>.json (default: $ASPACE_HOME/config.json,
$ASPACE_HOME defaults to ~/.aspace). --server/--key or $ASPACE_SERVER/$ASPACE_KEY override.
Session state lives in $ASPACE_HOME/state/poller-sessions.json, one session per agent.

Runs forever: long-poll → claim → build prompt → generate reply → finish → repeat.
Groups are the only channel: a wakeup always belongs to a group, and finishing
with --result replied posts the reply into that group.
No dependencies beyond Python 3.8+ stdlib. Ctrl-C to stop.

Reply backends (--backend, or $ASPACE_BACKEND):
  hermes     run `hermes chat` headless. One global Hermes session per agent, so
             all @-mentions continue the same conversation (prompt includes group name).
             Defaults are isolated: --ignore-rules (no personal memory/AGENTS.md)
             and toolsets "web". Override with $ASPACE_HERMES_TOOLSETS,
             $ASPACE_HERMES_ARGS (extra args, shell-split), $ASPACE_HERMES_KEEP_RULES=1.
  anthropic  call the Anthropic Messages API directly (needs $ANTHROPIC_API_KEY;
             model from $ASPACE_ANTHROPIC_MODEL, base from $ANTHROPIC_BASE_URL).
  cmd        run $ASPACE_REPLY_CMD with the prompt on stdin; stdout is the reply.
Default: cmd if $ASPACE_REPLY_CMD is set, otherwise hermes.

If generation fails the task is finished as `failed` (never a placeholder reply),
so the sender sees the failure instead of a fake answer.
"""
import json, os, sys, urllib.request, urllib.error, urllib.parse, ssl, time, subprocess, signal, logging, re, shlex, pathlib

logging.basicConfig(format="%(asctime)s %(levelname)s %(message)s", level=logging.INFO)
log = logging.getLogger("poller")

_ctx = ssl.create_default_context()
_running = True
REPLY_TIMEOUT = int(os.environ.get("ASPACE_REPLY_TIMEOUT", "150"))  # claim expires at 180s

def stop(*_):
    global _running
    _running = False
    log.info("shutting down...")

signal.signal(signal.SIGINT, stop)
signal.signal(signal.SIGTERM, stop)

# ── HTTP helpers ────────────────────────────────────────────────────────────

def api(method, server, key, path, body=None):
    url = server.rstrip("/") + path
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Authorization", f"Bearer {key}")
    req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=90, context=_ctx) as resp:
        return json.loads(resp.read())

def api_safe(method, server, key, path, body=None):
    try:
        return api(method, server, key, path, body)
    except urllib.error.HTTPError as e:
        raw = e.read().decode()
        log.error(f"{method} {path}: HTTP {e.code}: {raw[:200]}")
        return None
    except Exception as e:
        log.error(f"{method} {path}: {e}")
        return None

# ── state (agent → global backend session) ─────────────────────────────────

def state_path():
    # Session state lives with the config: $ASPACE_HOME/state/ (default ~/.aspace/state/).
    home = os.environ.get("ASPACE_HOME") or os.path.join(pathlib.Path.home(), ".aspace")
    return os.path.join(home, "state", "poller-sessions.json")

def load_state():
    try:
        with open(state_path()) as f:
            return json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        return {}

def save_state(st):
    p = state_path()
    os.makedirs(os.path.dirname(p), 0o700, exist_ok=True)
    tmp = p + ".tmp"
    with open(tmp, "w") as f:
        json.dump(st, f, indent=2)
    os.replace(tmp, p)

def config_dir():
    """Same config location as the Go CLI: $ASPACE_HOME, default ~/.aspace."""
    return os.environ.get("ASPACE_HOME") or os.path.join(pathlib.Path.home(), ".aspace")

def load_profile(profile):
    """{'server','key'} from $ASPACE_HOME/profiles/<id>.json, or config.json."""
    p = os.path.join(config_dir(), "profiles", f"{profile}.json") if profile \
        else os.path.join(config_dir(), "config.json")
    with open(p) as f:
        return json.load(f)

# ── prompt ──────────────────────────────────────────────────────────────────

def build_prompt(me, msg, history, item_context, group_name):
    """Prompt for one wakeup. Includes group name so the model knows context."""
    sender = msg.get("from", "unknown")
    topic = msg.get("task") or "讨论"
    lines = []
    for gm in history[-15:]:
        if gm.get("id") == msg.get("id"):
            continue
        lines.append(f"[{gm.get('from', '?')}]: {gm.get('body', '')[:800]}")

    parts = [f"你是共享工作空间里的 Agent「{me}」"]
    if group_name:
        parts[0] += f"，在群组「{group_name}」中被 @{sender} 提及（话题：{topic}）。"
    else:
        parts[0] += f"，被 @{sender} 提及（话题：{topic}）。"

    if item_context:
        parts.append("事项背景：\n" + item_context[:3000])
    if lines:
        parts.append("群内对话：\n" + "\n".join(lines))
    parts.append(f"{sender} 对你说：\n{msg.get('body', '')}")
    parts.append("直接给出你在群里的回复正文：简洁、有观点、有依据；不重复别人已说的；"
                 "不要寒暄，不要说明你是 AI，不要输出与回复无关的内容。")
    return "\n\n".join(parts)

# ── backends ────────────────────────────────────────────────────────────────

_SID = re.compile(r"^session_id:\s*(\S+)", re.M)

def gen_hermes(prompt, session):
    """Run hermes headless. Returns (reply, session_id)."""
    exe = os.environ.get("ASPACE_HERMES_BIN", "hermes")
    cmd = [exe, "chat", "--query-file", "-", "-Q", "--source", "tool"]
    if session:
        # Resume keeps the launch policy (toolsets/rules) of the original session.
        cmd += ["--resume", session]
    else:
        cmd += ["-t", os.environ.get("ASPACE_HERMES_TOOLSETS", "web")]
        if os.environ.get("ASPACE_HERMES_KEEP_RULES") != "1":
            cmd.append("--ignore-rules")
        cmd += shlex.split(os.environ.get("ASPACE_HERMES_ARGS", ""))
    r = subprocess.run(cmd, input=prompt, capture_output=True, text=True, timeout=REPLY_TIMEOUT)
    m = _SID.search(r.stderr) or _SID.search(r.stdout)
    sid = m.group(1) if m else session
    out = _SID.sub("", r.stdout).strip()
    if r.returncode != 0 or not out:
        raise RuntimeError(f"hermes rc={r.returncode}: {(r.stderr or r.stdout).strip()[-300:]}")
    return out, sid

def gen_anthropic(prompt, history_msgs):
    """Anthropic Messages API over urllib. history_msgs: prior [{role, content}]."""
    key = os.environ.get("ANTHROPIC_API_KEY")
    if not key:
        raise RuntimeError("ANTHROPIC_API_KEY not set")
    base = os.environ.get("ANTHROPIC_BASE_URL", "https://api.anthropic.com").rstrip("/")
    body = {
        "model": os.environ.get("ASPACE_ANTHROPIC_MODEL", "claude-sonnet-4-5"),
        "max_tokens": 1500,
        "messages": history_msgs + [{"role": "user", "content": prompt}],
    }
    req = urllib.request.Request(base + "/v1/messages", data=json.dumps(body).encode(), method="POST")
    req.add_header("x-api-key", key)
    req.add_header("anthropic-version", "2023-06-01")
    req.add_header("content-type", "application/json")
    with urllib.request.urlopen(req, timeout=REPLY_TIMEOUT, context=_ctx) as resp:
        data = json.loads(resp.read())
    out = "".join(b.get("text", "") for b in data.get("content", []) if b.get("type") == "text").strip()
    if not out:
        raise RuntimeError("empty response")
    return out

def gen_cmd(prompt):
    cmd = os.environ.get("ASPACE_REPLY_CMD")
    if not cmd:
        raise RuntimeError("ASPACE_REPLY_CMD not set")
    r = subprocess.run(cmd, shell=True, input=prompt, capture_output=True, text=True, timeout=REPLY_TIMEOUT)
    if r.returncode != 0 or not r.stdout.strip():
        raise RuntimeError(f"reply command rc={r.returncode}: {r.stderr.strip()[-300:]}")
    return r.stdout.strip()

# ── core ────────────────────────────────────────────────────────────────────

def process_task(server, key, me, consumer, backend, task):
    wakeup_id = task["id"]
    group_id = task.get("group_id", "")
    log.info(f"processing {wakeup_id}: msg={task['message_id']} group={group_id} reason={task.get('reason','')}")

    claim = api_safe("POST", server, key, f"/v1/wakeups/{wakeup_id}/claim", {"consumer": consumer})
    token = (claim or {}).get("claim_token")
    if not token:
        log.warning(f"could not claim {wakeup_id}, skipping")
        return

    def fail(reason):
        log.error(f"{wakeup_id} failed: {reason}")
        api_safe("POST", server, key, f"/v1/wakeups/{wakeup_id}/finish",
                 {"claim_token": token, "result": "failed", "error_summary": reason[:500]})

    if not group_id:
        return fail("wakeup has no group; groups are the only channel")
    # Server returns oldest-first; take the tail. The original message we were
    # woken for is in the same list, so one request covers both.
    history = api_safe("GET", server, key, f"/v1/groups/{urllib.parse.quote(group_id)}/messages?limit=1000")
    if history is None:
        return fail("could not read the group (removed from it?)")
    msg = next((m for m in history if m.get("id") == task["message_id"]), None)
    if not msg:
        return fail("could not find the original message in the group")
    grp = api_safe("GET", server, key, f"/v1/groups/{urllib.parse.quote(group_id)}")
    group_name = (grp or {}).get("topic", "")

    item_context = ""
    if msg.get("item"):
        it = api_safe("GET", server, key, f"/v1/items/{urllib.parse.quote(msg['item'])}")
        item_context = (it or {}).get("context", "")

    state = load_state()
    skey = f"{backend}:{me}"  # One session per agent
    sess = state.get(skey, {})
    prompt = build_prompt(me, msg, history, item_context, group_name)

    try:
        if backend == "hermes":
            try:
                reply, sid = gen_hermes(prompt, sess.get("session"))
            except RuntimeError as e:
                if not sess.get("session"):
                    raise
                log.warning(f"resume failed ({e}), starting a fresh session")
                reply, sid = gen_hermes(prompt, None)
            sess["session"] = sid
        elif backend == "anthropic":
            turns = sess.get("turns", [])[-20:]
            reply = gen_anthropic(prompt, turns)
            sess["turns"] = turns + [{"role": "user", "content": prompt}, {"role": "assistant", "content": reply}]
            sess["session"] = "messages"
        else:
            reply = gen_cmd(prompt)
    except subprocess.TimeoutExpired:
        return fail(f"{backend} timed out after {REPLY_TIMEOUT}s")
    except Exception as e:
        return fail(f"{backend}: {e}")

    result = api_safe("POST", server, key, f"/v1/wakeups/{wakeup_id}/finish",
                      {"claim_token": token, "result": "replied", "body": reply})
    if not result:
        log.error(f"finish failed for {wakeup_id}")
        return
    if backend != "cmd":
        state[skey] = sess
        save_state(state)
    log.info(f"replied to {wakeup_id}: {result.get('reply_message_id', '')}")

# ── main ────────────────────────────────────────────────────────────────────

def main():
    import argparse
    p = argparse.ArgumentParser(description="Poll aspace wakeups and auto-reply")
    p.add_argument("--profile", default=os.environ.get("ASPACE_PROFILE", ""),
                   help="agent profile in $ASPACE_HOME/profiles/<id>.json (default: config.json)")
    p.add_argument("--server", default="", help="server URL (overrides the profile)")
    p.add_argument("--key", default="", help="API key (overrides the profile)")
    p.add_argument("--backend", default=os.environ.get("ASPACE_BACKEND") or ("cmd" if os.environ.get("ASPACE_REPLY_CMD") else "hermes"),
                   choices=["hermes", "anthropic", "cmd"])
    p.add_argument("--interval", type=int, default=30, help="long-poll wait in seconds (max 60)")
    p.add_argument("--consumer", default="", help="consumer name for claim (default: <agent>@<host>)")
    p.add_argument("--once", action="store_true", help="run one poll cycle and exit")
    args = p.parse_args()

    server = args.server or os.environ.get("ASPACE_SERVER", "")
    key = args.key or os.environ.get("ASPACE_KEY", "")
    if not server or not key:
        try:
            cfg = load_profile(args.profile)
        except (OSError, json.JSONDecodeError) as e:
            p.error(f"no --server/--key and cannot read profile from {config_dir()}: {e}")
        server = server or cfg.get("server", "")
        key = key or cfg.get("key", "")
    if not server or not key:
        p.error("--server and --key are required (or set ASPACE_SERVER / ASPACE_KEY)")

    who = api_safe("GET", server, key, "/v1/whoami")
    if not who:
        sys.exit("poller: cannot reach server or key rejected")
    me = who.get("name") or who.get("agent") or "agent"
    consumer = args.consumer or f"{me}@{os.uname().nodename.split('.')[0]}"
    log.info(f"starting poller: server={server} agent={me} backend={args.backend} consumer={consumer} state={state_path()}")

    backoff = 1
    while _running:
        try:
            tasks = api_safe("GET", server, key, f"/v1/wakeups?wait={min(args.interval, 60)}&limit=10") or []
            backoff = 1
            for task in tasks:
                if not _running:
                    break
                process_task(server, key, me, consumer, args.backend, task)
            if args.once:
                break
            if not tasks:
                time.sleep(1)  # guard against a server that ignores ?wait
        except Exception as e:
            log.error(f"poll error: {e}")
            backoff = min(backoff * 2, 60)
            time.sleep(backoff)
    log.info("poller stopped")

if __name__ == "__main__":
    main()

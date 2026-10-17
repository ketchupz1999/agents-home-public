#!/usr/bin/env python3
"""Exercise the documented local setup with temporary data, then stop it.

Requires Go, Git and Python 3. It never reads existing credentials, never
connects to a model or Git remote, and does not leave a running service.
"""
import http.cookiejar
import json
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

SOURCE = Path(__file__).resolve().parents[1]
HTTP = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def run(args, **kwargs):
    return subprocess.run(args, check=True, capture_output=True, text=True, **kwargs)


def main():
    with tempfile.TemporaryDirectory(prefix="agentshome-smoke-") as temp:
        work = Path(temp)
        binary = work / "aspace-server"
        run(["go", "build", "-o", str(binary), "./cmd/aspace-server"], cwd=SOURCE)
        memory = work / "memory"
        shutil.copytree(SOURCE / "templates/memory", memory)
        for args in (("init", "-q", "-b", "main"), ("add", "."), ("commit", "-qm", "Initialize private template")):
            run(["git", "-C", str(memory), "-c", "user.name=Test", "-c", "user.email=test@example.invalid", *args])
        keys = {}
        for name in ("owner", "my-agent", "outsider"):
            flags = ["--owner"] if name == "owner" else ["--read", "profile/,items/,agents/"]
            keys[name] = run([str(binary), "key", "add", "--keys", str(work / "keys.json"), "--name", name, *flags]).stdout.strip()
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        base = f"http://127.0.0.1:{port}"
        with (work / "server.log").open("w") as log:
            proc = subprocess.Popen([str(binary), "serve", "--repo", str(memory), "--keys", str(work / "keys.json"), "--db", str(work / "aspace.db"), "--no-remote", "--addr", f"127.0.0.1:{port}"], stdout=log, stderr=log)
            try:
                for _ in range(100):
                    if proc.poll() is not None:
                        raise RuntimeError("server exited during setup")
                    try:
                        with HTTP.open(base + "/healthz", timeout=1) as r:
                            if r.status == 200:
                                break
                    except (urllib.error.URLError, TimeoutError):
                        time.sleep(.1)
                else:
                    raise RuntimeError("server did not start")

                def api(who, method, path, body=None, expected=200):
                    data = None if body is None else json.dumps(body).encode()
                    req = urllib.request.Request(base + path, data=data, method=method)
                    if who:
                        req.add_header("Authorization", "Bearer " + keys[who])
                    req.add_header("Content-Type", "application/json")
                    try:
                        with HTTP.open(req, timeout=15) as response:
                            code, payload = response.status, response.read()
                    except urllib.error.HTTPError as error:
                        code, payload = error.code, error.read()
                    assert code == expected, (method, path, code, expected)
                    return json.loads(payload) if payload else None

                api(None, "GET", "/v1/now", expected=401)
                assert api("my-agent", "GET", "/v1/whoami")["name"] == "my-agent"
                group = api("owner", "POST", "/v1/groups", {"item": "welcome", "topic": "Local smoke", "members": ["my-agent"], "body": "PRIVATE_MARKER", "mentions": ["my-agent"]}, 201)
                gid = group["group"]["id"]
                task = api("my-agent", "GET", "/v1/wakeups")[0]
                token = api("my-agent", "POST", f'/v1/wakeups/{task["id"]}/claim', {})["claim_token"]
                api("my-agent", "POST", f'/v1/wakeups/{task["id"]}/finish', {"claim_token": "wrong", "result": "replied", "body": "must not persist"}, 409)
                assert len(api("owner", "GET", f"/v1/groups/{gid}/messages")) == 1
                finished = api("my-agent", "POST", f'/v1/wakeups/{task["id"]}/finish', {"claim_token": token, "result": "replied", "body": "Completed from a real HTTP client"})
                assert finished["status"] == "replied"
                messages = api("owner", "GET", f"/v1/groups/{gid}/messages")
                assert len(messages) == 2 and messages[0]["id"] == group["root_message_id"]
                api("outsider", "GET", f"/v1/groups/{gid}/messages", expected=404)
                assert api("outsider", "GET", "/v1/search?q=PRIVATE_MARKER")["hits"] == []
                api("owner", "POST", f"/v1/groups/{gid}/members", {"remove": ["my-agent"]})
                api("my-agent", "GET", f"/v1/groups/{gid}/messages", expected=404)
                browser = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
                req = urllib.request.Request(base + "/d/login", data=urllib.parse.urlencode({"key": keys["owner"]}).encode())
                with browser.open(req, timeout=15) as response:
                    assert response.geturl().endswith("/d/")
                    assert "我的空间" in response.read().decode()
                print("PASS: template setup, auth, group, claim/finish, rollback, ordering, scopes, revocation, owner login")
            finally:
                proc.terminate()
                try:
                    proc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait()


if __name__ == "__main__":
    main()

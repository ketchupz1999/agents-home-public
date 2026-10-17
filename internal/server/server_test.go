package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ketchupz1999/agents-home-public/internal/db"
	"github.com/ketchupz1999/agents-home-public/internal/protocol"
	"github.com/ketchupz1999/agents-home-public/internal/store"
	"github.com/ketchupz1999/agents-home-public/internal/web"
)

type env struct {
	ts    *httptest.Server
	keys  map[string]string
	seed  string // second clone, played by a "local agent" using plain git
	agent string // third clone, used to push agent branches
	db    *db.DB
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func setup(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	git(t, root, "init", "--bare", "-b", "main", origin)
	seed := filepath.Join(root, "seed")
	git(t, root, "clone", "-q", origin, seed)
	for p, c := range map[string]string{
		"agents/registry.yaml":     "agents: []\n",
		"profile/personal.md":      "# me\n",
		"items/english/CONTEXT.md": "# english v1\n",
		"items/secret/CONTEXT.md":  "# not for muse\n",
		"agents/muse/card.md":      "# muse\n",
		"goals/travel.md":          "---\ntype: goal\nkind: outcome\npeople: [partner]\n---\n# 共同旅行\n",
		"lines/tibet.md":           "---\ntype: line\nrole: main\nserves: [travel]\n---\n# 西藏线\n",
		"people/partner.md":        "---\ntype: person\ndisplay_name: 对象\ntier: core\n---\n",
		"profile/preferences.md":   "# 偏好\n",
	} {
		os.MkdirAll(filepath.Join(seed, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(seed, p), []byte(c), 0o644)
	}
	git(t, seed, "add", "-A")
	git(t, seed, "commit", "-qm", "seed")
	git(t, seed, "push", "-q", "origin", "main")

	srvDir := filepath.Join(root, "srv")
	git(t, root, "clone", "-q", origin, srvDir)
	agentDir := filepath.Join(root, "agentclone")
	git(t, root, "clone", "-q", origin, agentDir)

	kf, _ := LoadKeys(filepath.Join(root, "keys.json"))
	keys := map[string]string{}
	keys["owner"], _ = kf.Add("owner", true, false, nil)
	keys["muse"], _ = kf.Add("assistant-cloud", false, true, []string{"*"})
	keys["claude-code"], _ = kf.Add("claude-code", false, false, []string{"*"})
	keys["limited"], _ = kf.Add("limited", false, false, []string{"profile/", "items/english/"})

	d, err := db.Open(filepath.Join(root, "aspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: &store.Store{Dir: srvDir, Remote: true}, Keys: kf, DB: d, Now: time.Now}
	s.Web = &web.Web{Store: s.Store, DB: d, Now: time.Now, SecretKey: kf.OwnerHash(),
		Auth: func(secret string) (string, bool) {
			if k := kf.Lookup(secret); k != nil && k.Owner {
				return k.Name, true
			}
			return "", false
		},
		CreateGroup:        s.CreateGroupAs,
		UpdateGroupMembers: s.UpdateGroupMembersAs,
		KeyNames: func() []string {
			var names []string
			for _, k := range kf.List() {
				names = append(names, k.Name)
			}
			return names
		}}
	e := &env{ts: httptest.NewServer(s.Handler()), keys: keys, seed: seed, agent: agentDir, db: d}
	t.Cleanup(func() { e.ts.Close(); d.Close() })
	return e
}

func (e *env) do(t *testing.T, key, method, path string, in any) (int, map[string]any, []any) {
	t.Helper()
	var body *bytes.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	} else {
		body = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, body)
	req.Header.Set("Authorization", "Bearer "+e.keys[key])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw json.RawMessage
	json.NewDecoder(resp.Body).Decode(&raw)
	var obj map[string]any
	var arr []any
	if json.Unmarshal(raw, &obj) != nil {
		json.Unmarshal(raw, &arr)
	}
	return resp.StatusCode, obj, arr
}

func (e *env) pushBranch(t *testing.T, branch string, files map[string]string) {
	t.Helper()
	git(t, e.agent, "checkout", "-q", "main")
	git(t, e.agent, "pull", "-q", "--rebase")
	git(t, e.agent, "checkout", "-q", "-b", branch)
	for p, c := range files {
		os.MkdirAll(filepath.Join(e.agent, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(e.agent, p), []byte(c), 0o644)
	}
	git(t, e.agent, "add", "-A")
	git(t, e.agent, "commit", "-qm", "change")
	git(t, e.agent, "push", "-q", "-u", "origin", branch)
}

// Group traffic lives in SQLite, not in git: nothing about it should become a
// commit, and identity always comes from the key.
func TestGroupTrafficStaysOutOfGit(t *testing.T) {
	e := setup(t)
	code, res, _ := e.do(t, "claude-code", "POST", "/v1/groups", map[string]any{
		"item": "english", "topic": "give advice", "members": []string{"assistant-cloud"},
		"body": "summary", "mentions": []string{"assistant-cloud"}, "from": "owner", "requested_by": "owner",
	})
	if code != 201 {
		t.Fatalf("create: %d %v", code, res)
	}
	gid := res["group"].(map[string]any)["id"].(string)
	rootID := res["root_message_id"].(string)
	opening, err := e.db.GetMessage(rootID)
	if err != nil || opening.From != "claude-code" || opening.RequestedBy != "agent" {
		t.Fatalf("identity must come from the key: %+v %v", opening, err)
	}

	// Unread is per identity, and acking one message clears only that one.
	if _, _, msgs := e.do(t, "muse", "GET", "/v1/groups/"+gid+"/messages", nil); len(msgs) != 1 || msgs[0].(map[string]any)["read"] != false {
		t.Fatalf("muse should see one unread message: %v", msgs)
	}
	if code, _, _ := e.do(t, "muse", "POST", "/v1/groups/"+gid+"/messages/"+rootID+"/ack", nil); code != 200 {
		t.Fatalf("ack: %d", code)
	}
	if _, _, msgs := e.do(t, "muse", "GET", "/v1/groups/"+gid+"/messages", nil); msgs[0].(map[string]any)["read"] != true {
		t.Fatalf("ack did not stick: %v", msgs)
	}
	// claude-code never read it, so its own view is still unread.
	if _, _, msgs := e.do(t, "claude-code", "GET", "/v1/groups/"+gid+"/messages", nil); msgs[0].(map[string]any)["read"] != false {
		t.Fatalf("read state leaked across identities: %v", msgs)
	}
	// Acking a message from another group is refused.
	if code, _, _ := e.do(t, "muse", "POST", "/v1/groups/grp_other/messages/"+rootID+"/ack", nil); code != 404 {
		t.Fatalf("cross-group ack: %d", code)
	}
	// The owner is still recorded as the requester when they post.
	code, res, _ = e.do(t, "owner", "POST", "/v1/groups/"+gid+"/messages", map[string]any{"body": "我来说两句"})
	if code != 201 {
		t.Fatalf("owner send: %d %v", code, res)
	}
	sentID, _ := res["id"].(string)
	if m, _ := e.db.GetMessage(sentID); m == nil || m.RequestedBy != protocol.RequestedByOwner {
		t.Fatalf("owner message requester: %+v", m)
	}
	if log := git(t, e.seed, "log", "--oneline"); strings.Contains(log, "inbox") || strings.Contains(log, "ack") || strings.Contains(log, "give advice") {
		t.Fatalf("group traffic leaked into git history:\n%s", log)
	}
}

func TestReadScope(t *testing.T) {
	e := setup(t)
	if code, _, _ := e.do(t, "limited", "GET", "/v1/file?path=items/secret/CONTEXT.md", nil); code != 404 {
		t.Fatalf("limited read secret: %d", code)
	}
	if code, _, _ := e.do(t, "limited", "POST", "/v1/groups", map[string]any{"item": "secret", "topic": "x", "members": []string{"owner"}, "body": "x"}); code != 404 {
		t.Fatalf("limited group on a hidden item: %d", code)
	}
	if code, _, _ := e.do(t, "limited", "GET", "/v1/file?path=../keys.json", nil); code != 404 {
		t.Fatalf("traversal: %d", code)
	}
	// Any agent may write its own card.
	if code, _, _ := e.do(t, "limited", "PUT", "/v1/cards/me", map[string]any{"content": "hi\n"}); code != 200 {
		t.Fatalf("card: %d", code)
	}
}

// Applying rebases onto main and fast-forwards: linear history, no merge
// commits, branch deleted afterwards.
func TestBranchApplyKeepsHistoryLinear(t *testing.T) {
	e := setup(t)
	e.pushBranch(t, "agent/assistant-cloud/practice-rhythm", map[string]string{
		"goals/travel.md":          "---\ntype: goal\nkind: outcome\npeople: [partner]\n---\n# 共同旅行（更新）\n",
		"items/english/CONTEXT.md": "# english v2\n",
	})

	_, _, pending := e.do(t, "claude-code", "GET", "/v1/branches?status=pending", nil)
	if len(pending) != 1 {
		t.Fatalf("pending: %v", pending)
	}
	req, _ := http.NewRequest("GET", e.ts.URL+"/v1/branches/agent/assistant-cloud/practice-rhythm/diff", nil)
	req.Header.Set("Authorization", "Bearer "+e.keys["owner"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	resp.Body.Close()
	if !strings.Contains(buf.String(), "# english v2") {
		t.Fatalf("diff should show the change:\n%s", buf.String())
	}

	if code, _, _ := e.do(t, "claude-code", "POST", "/v1/branches/agent/assistant-cloud/practice-rhythm/apply", nil); code != 403 {
		t.Fatalf("non-approver apply: %d", code)
	}
	if code, res, _ := e.do(t, "muse", "POST", "/v1/branches/agent/assistant-cloud/practice-rhythm/apply", nil); code != 200 || res["status"] != "applied" {
		t.Fatalf("apply: %d %v", code, res)
	}
	_ = e

	_, f, _ := e.do(t, "owner", "GET", "/v1/file?path=items/english/CONTEXT.md", nil)
	if f["content"] != "# english v2\n" {
		t.Fatalf("content not applied: %v", f["content"])
	}
	if out := git(t, e.seed, "log", "--oneline", "--merges"); strings.TrimSpace(out) != "" {
		t.Fatalf("merge commit appeared:\n%s", out)
	}
	// NOW.md followed the merge in the same push.
	_, nowFile, _ := e.do(t, "owner", "GET", "/v1/file?path=NOW.md", nil)
	if !strings.Contains(nowFile["content"].(string), "共同旅行（更新）") {
		t.Fatalf("NOW.md not refreshed:\n%s", nowFile["content"])
	}
	git(t, e.seed, "fetch", "-q", "--prune", "origin")
	if branches := git(t, e.seed, "branch", "-r"); strings.Contains(branches, "practice-rhythm") {
		t.Fatalf("branch not deleted: %s", branches)
	}
	if _, _, pending := e.do(t, "owner", "GET", "/v1/branches?status=pending", nil); len(pending) != 0 {
		t.Fatalf("pending after apply: %v", pending)
	}
}

// A conflict leaves the branch pending and main untouched.
func TestBranchConflictIsReported(t *testing.T) {
	e := setup(t)
	e.pushBranch(t, "agent/assistant-cloud/conflict", map[string]string{"items/english/CONTEXT.md": "# branch\n"})

	git(t, e.seed, "pull", "-q", "--rebase")
	os.WriteFile(filepath.Join(e.seed, "items/english/CONTEXT.md"), []byte("# main\n"), 0o644)
	git(t, e.seed, "commit", "-qam", "main change")
	git(t, e.seed, "push", "-q")

	if code, res, _ := e.do(t, "owner", "POST", "/v1/branches/agent/assistant-cloud/conflict/apply", nil); code != 409 {
		t.Fatalf("conflict apply: %d %v", code, res)
	}
	_, f, _ := e.do(t, "owner", "GET", "/v1/file?path=items/english/CONTEXT.md", nil)
	if f["content"] != "# main\n" {
		t.Fatalf("main should be unchanged on conflict: %v", f["content"])
	}
	if _, _, pending := e.do(t, "owner", "GET", "/v1/branches?status=pending", nil); len(pending) != 1 {
		t.Fatalf("conflicting branch should stay pending: %v", pending)
	}
}

func TestBranchRejectDeletesIt(t *testing.T) {
	e := setup(t)
	e.pushBranch(t, "agent/assistant-cloud/noisy", map[string]string{"goals/travel.md": "# nope\n"})
	if code, res, _ := e.do(t, "owner", "POST", "/v1/branches/agent/assistant-cloud/noisy/reject", nil); code != 200 || res["status"] != "rejected" {
		t.Fatalf("reject: %d %v", code, res)
	}
	if _, _, pending := e.do(t, "owner", "GET", "/v1/branches?status=pending", nil); len(pending) != 0 {
		t.Fatalf("pending after reject: %v", pending)
	}
	_, f, _ := e.do(t, "owner", "GET", "/v1/file?path=goals/travel.md", nil)
	if c := f["content"].(string); !strings.Contains(c, "共同旅行") || strings.Contains(c, "nope") {
		t.Fatalf("rejected change leaked: %v", c)
	}
}

func TestBriefInheritsPeopleAndChain(t *testing.T) {
	e := setup(t)
	e.pushBranch(t, "agent/assistant-cloud/add-trip-item", map[string]string{
		"items/trip/CONTEXT.md": "---\ntype: item\nserves: [tibet]\nreads: [notes.md, gone.md]\n---\n# 十一出行\n",
		"items/trip/notes.md":   "n\n",
	})
	if code, _, _ := e.do(t, "owner", "POST", "/v1/branches/agent/assistant-cloud/add-trip-item/apply", nil); code != 200 {
		t.Fatalf("apply trip item: %d", code)
	}
	_, b, _ := e.do(t, "claude-code", "GET", "/v1/brief/trip", nil)
	chain := b["chain"].([]any)
	if len(chain) != 2 || chain[0].(map[string]any)["path"] != "lines/tibet.md" {
		t.Fatalf("chain: %v", chain)
	}
	if ppl := b["people"].([]any); len(ppl) != 1 || ppl[0].(map[string]any)["path"] != "people/partner.md" {
		t.Fatalf("people inherited from goal: %v", ppl)
	}
	reads := b["reads"].([]any)
	if len(reads) != 2 || reads[1].(map[string]any)["note"] != "missing" {
		t.Fatalf("reads: %v", reads)
	}
}

func TestBodyLimitAndWhoami(t *testing.T) {
	e := setup(t)
	if code, _, _ := e.do(t, "owner", "POST", "/v1/groups", map[string]any{
		"item": "english", "topic": "t", "members": []string{"assistant-cloud"}, "body": strings.Repeat("x", maxBody+1)}); code != 400 {
		t.Fatalf("oversized body: %d", code)
	}
	req, _ := http.NewRequest("GET", e.ts.URL+"/v1/whoami", nil)
	req.Header.Set("Authorization", "Bearer nope")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("bad key: %d", resp.StatusCode)
	}
	_, me, _ := e.do(t, "muse", "GET", "/v1/whoami", nil)
	if me["name"] != "assistant-cloud" || me["approve"] != true {
		t.Fatalf("whoami: %v", me)
	}
	_, me, _ = e.do(t, "claude-code", "GET", "/v1/whoami", nil)
	if me["approve"] != false {
		t.Fatalf("whoami: %v", me)
	}
}

func TestSearch(t *testing.T) {
	e := setup(t)
	e.pushBranch(t, "agent/assistant-cloud/notes", map[string]string{
		"knowledge/personal/insurance.md": "# 保险\n\n河南惠民保 2026 年度参保核实\n其他内容\n",
		"people/father.md":                "---\ntype: person\n---\n爸爸的惠民保\n",
	})
	if code, _, _ := e.do(t, "owner", "POST", "/v1/branches/agent/assistant-cloud/notes/apply", nil); code != 200 {
		t.Fatalf("apply: %d", code)
	}
	e.do(t, "owner", "POST", "/v1/groups", map[string]any{"item": "english", "topic": "看看惠民保", "members": []string{"assistant-cloud"}, "body": "x"})

	_, res, _ := e.do(t, "owner", "GET", "/v1/search?q=%E6%83%A0%E6%B0%91%E4%BF%9D", nil)
	hits := res["hits"].([]any)
	kinds := map[string]int{}
	for _, h := range hits {
		kinds[h.(map[string]any)["kind"].(string)]++
	}
	if kinds["file"] != 2 || kinds["message"] != 1 {
		t.Fatalf("owner hits: %v", hits)
	}
	first := hits[0].(map[string]any)
	if first["path"] != "knowledge/personal/insurance.md" || first["line"].(float64) != 3 {
		t.Fatalf("first hit: %v", first)
	}

	// Read scope applies: limited cannot see people/ or knowledge/.
	_, res, _ = e.do(t, "limited", "GET", "/v1/search?q=%E6%83%A0%E6%B0%91%E4%BF%9D", nil)
	for _, h := range res["hits"].([]any) {
		if p, _ := h.(map[string]any)["path"].(string); strings.HasPrefix(p, "people/") || strings.HasPrefix(p, "knowledge/") {
			t.Fatalf("limited saw %s", p)
		}
	}
	// Scope narrows; case-insensitive for ASCII; too-short queries rejected.
	_, res, _ = e.do(t, "owner", "GET", "/v1/search?q=%E6%83%A0%E6%B0%91%E4%BF%9D&in=people", nil)
	if n := len(res["hits"].([]any)); n != 1 {
		t.Fatalf("scoped hits: %d", n)
	}
	_, res, _ = e.do(t, "owner", "GET", "/v1/search?q=ENGLISH", nil)
	if len(res["hits"].([]any)) == 0 {
		t.Fatalf("case-insensitive search found nothing")
	}
	for _, in := range []string{"..", "../..", ".git", "/etc"} {
		code, res, _ := e.do(t, "owner", "GET", "/v1/search?q=root&in="+in, nil)
		if code == 200 && len(res["hits"].([]any)) > 0 {
			t.Fatalf("scope %q escaped the repo: %v", in, res["hits"])
		}
	}
	if code, _, _ := e.do(t, "owner", "GET", "/v1/search?q=a", nil); code != 400 {
		t.Fatalf("short query: %d", code)
	}
}

// The dashboard is owner-only: no session redirects to login, a wrong key is
// rejected, and the right key can read the overview and merge a branch.
func TestDashboard(t *testing.T) {
	e := setup(t)
	e.pushBranch(t, "agent/assistant-cloud/via-dashboard", map[string]string{"goals/travel.md": "# 通过面板改的\n"})

	// Not logged in → redirect to login.
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(e.ts.URL + "/d/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 303 {
		t.Fatalf("anonymous /d/: %d", resp.StatusCode)
	}
	// Wrong key rejected.
	resp, _ = http.PostForm(e.ts.URL+"/d/login", url.Values{"key": {"nope"}})
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("bad login: %d", resp.StatusCode)
	}
	// Owner key logs in and gets a session cookie.
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err = c.PostForm(e.ts.URL+"/d/login", url.Values{"key": {e.keys["owner"]}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	body := get(t, c, e.ts.URL+"/d/")
	for _, want := range []string{"共享空间", "共同旅行", "待确认"} {
		if !strings.Contains(body, want) {
			t.Fatalf("overview missing %q", want)
		}
	}
	// The dashboard can merge a branch, and the result shows up.
	body = get(t, c, e.ts.URL+"/d/branches")
	if !strings.Contains(body, "via-dashboard") {
		t.Fatalf("pending branch not listed: %s", body[:200])
	}
	resp, err = c.PostForm(e.ts.URL+"/d/branch/agent/assistant-cloud/via-dashboard/apply", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	_, f, _ := e.do(t, "owner", "GET", "/v1/file?path=goals/travel.md", nil)
	if !strings.Contains(f["content"].(string), "通过面板改的") {
		t.Fatalf("merge via dashboard did not land: %v", f["content"])
	}
	// Item page renders a brief-style page for one item.
	if b := get(t, c, e.ts.URL+"/d/item/english"); !strings.Contains(b, "english v1") {
		t.Fatalf("item page: %s", b[:200])
	}
}

func get(t *testing.T, c *http.Client, url string) string {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d", url, resp.StatusCode)
	}
	return string(b)
}

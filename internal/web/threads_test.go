package web

import (
	"net/http"
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
)

func threadMessage(id, parent string, minute int) protocol.Message {
	return protocol.Message{ID: id, ReplyTo: parent, From: "claude-local", To: "assistant-cloud", Item: "agent-hub", Task: "Re: Re: 讨论主题", Body: "**正文直接可见**", CreatedAt: time.Date(2026, 10, 10, 9, minute, 0, 0, time.UTC), Kind: "reply"}
}

func TestConversationGroupingAndFiltering(t *testing.T) {
	root := threadMessage("root", "", 0)
	root.Task = "Re: re: RE: 根标题"
	child := threadMessage("child", "root", 2)
	grandchild := threadMessage("grandchild", "child", 2)
	grandchild.Item = "other"
	other := threadMessage("separate", "", 3)
	threads := groupConversations([]protocol.Message{grandchild, other, child, root}, func(id string) bool { return id != "grandchild" })
	if len(threads) != 2 || threads[0].RootID != "separate" {
		t.Fatalf("last activity order: %+v", threads)
	}
	c := threads[1]
	if c.RootID != "root" || c.Title != "根标题" || len(c.Messages) != 3 || c.Messages[0].ID != "root" || c.Unread != 1 {
		t.Fatalf("reply chain: %+v", c)
	}
}

func TestConversationMissingParentAndCycle(t *testing.T) {
	messages := []protocol.Message{threadMessage("x", "missing", 0), threadMessage("y", "missing", 1), threadMessage("a", "b", 2), threadMessage("b", "a", 3), threadMessage("c", "b", 4)}
	threads := groupConversations(messages, func(string) bool { return false })
	if len(threads) != 2 {
		t.Fatalf("missing/cycle groups=%d", len(threads))
	}
	byRoot := map[string]int{}
	for _, c := range threads {
		byRoot[c.RootID] = len(c.Messages)
	}
	if byRoot["missing"] != 2 || byRoot["a"] != 3 {
		t.Fatalf("unstable roots: %v", byRoot)
	}
}

func testThreadWeb(t *testing.T) *Web {
	t.Helper()
	root := t.TempDir()
	webTestGit(t, root, "init", "-q", "-b", "main")
	if err := os.MkdirAll(filepath.Join(root, "agents"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agents/registry.yaml"), []byte("agents: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	webTestGit(t, root, "add", "agents/registry.yaml")
	webTestGit(t, root, "commit", "-qm", "fixture")
	database, err := db.Open(filepath.Join(root, "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return &Web{DB: database, Store: &store.Store{Dir: root}, SecretKey: []byte("test-only-secret"), Now: time.Now}
}
func requestThread(t *testing.T, w *Web, method, target string, values url.Values, authenticated bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(values.Encode()))
	if method == "POST" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if authenticated {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: w.token()})
	}
	rec := httptest.NewRecorder()
	w.Handler().ServeHTTP(rec, req)
	return rec
}
func putThread(t *testing.T, w *Web, m protocol.Message) {
	t.Helper()
	if err := w.DB.PutMessage(m); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityCardFallbackAndValidation(t *testing.T) {
	w := testThreadWeb(t)
	path := filepath.Join(w.Store.Dir, "agents", "new-agent")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	raw := "---\ndisplay_name: '<script>custom</script>'\navatar_text: '新'\navatar_color: '#FFFFFF'\n---\n# Card"
	identityFile(t, w, "agents/new-agent/card.md", []byte(raw))
	markup := avatar(w.identity("new-agent"))
	if !strings.Contains(markup, "&lt;script&gt;") || strings.Contains(markup, "<script>") || !strings.Contains(markup, "color:#17221B") {
		t.Fatal(markup)
	}
	unknown := w.identity("unknown")
	if unknown.Name != "unknown" || unknown.Text != "U" || unknown.Color != defaultIdentity("unknown").Color {
		t.Fatal("unknown identity not stable")
	}
	if unsafe := avatar(agentIdentity{Name: "x", Text: "x", Color: `red; background:url(https://x)`}); strings.Contains(unsafe, "url(") {
		t.Fatal("CSS injection")
	}
}

func webTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	all := append([]string{"-c", "user.name=test", "-c", "user.email=test@local"}, args...)
	cmd := exec.Command("git", all...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

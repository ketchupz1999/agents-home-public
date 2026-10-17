package web

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFocusOwnerValidationAndRecovery(t *testing.T) {
	w := testThreadWeb(t)
	for _, id := range []string{"a", "b"} {
		identityFile(t, w, "items/"+id+"/CONTEXT.md", []byte("---\ntype: item\n---\n# "+id+"\n"))
	}
	values := func(items ...string) url.Values {
		return url.Values{"item": items, "base_revision": {"0"}, "csrf": {w.focusCSRF()}}
	}
	v := values("a", "b")
	if got := requestThread(t, w, "POST", "/d/focus", v, false); got.Code != 303 {
		t.Fatal(got.Code)
	}
	if f, _ := w.DB.GetFocus(); f.Revision != 0 {
		t.Fatal("unauth saved")
	}
	bad := values("a")
	bad.Set("csrf", w.token())
	if got := requestThread(t, w, "POST", "/d/focus", bad, true); got.Code != 403 {
		t.Fatal("session used as csrf", got.Code)
	}
	for _, ids := range [][]string{{"a", "a"}, {"missing"}, {"a", "b", "c"}} {
		if got := requestThread(t, w, "POST", "/d/focus", values(ids...), true); got.Code != 400 {
			t.Fatal(ids, got.Code)
		}
	}
	if got := requestThread(t, w, "POST", "/d/focus", v, true); got.Code != 303 {
		t.Fatal(got.Code, got.Body.String())
	}
	if got := requestThread(t, w, "POST", "/d/focus", v, true); got.Code != 303 {
		t.Fatal("retry", got.Code)
	}
	got := requestThread(t, w, "POST", "/d/focus", values("b", "a"), true)
	if got.Code != 409 || !strings.Contains(got.Body.String(), `value="1"`) || !strings.Contains(got.Body.String(), `value="b" selected`) {
		t.Fatal("conflict must preserve draft", got.Code)
	}
	if err := os.Remove(filepath.Join(w.Store.Dir, "items/a/CONTEXT.md")); err != nil {
		t.Fatal(err)
	}
	if got := requestThread(t, w, "POST", "/d/focus", v, true); got.Code != 400 {
		t.Fatal("deleted same choice bypassed validation", got.Code)
	}
	clear := values("", "")
	clear.Set("base_revision", "1")
	if got := requestThread(t, w, "POST", "/d/focus", clear, true); got.Code != 303 {
		t.Fatal("clear", got.Code)
	}
	f, _ := w.DB.GetFocus()
	if f.Revision != 2 || len(f.Items) != 0 {
		t.Fatal(f)
	}
	if err := w.DB.Close(); err != nil {
		t.Fatal(err)
	}
	if got := requestThread(t, w, "POST", "/d/focus", clear, true); got.Code != 503 {
		t.Fatal("DB failure", got.Code)
	}
}
func TestOverviewHasNoDemoPinsAndEscapesContent(t *testing.T) {
	w := testThreadWeb(t)
	identityFile(t, w, "items/example/CONTEXT.md", []byte("# <script>alert(1)</script>\n"))
	got := requestThread(t, w, "GET", "/d/", nil, true)
	if got.Code != 200 || !strings.Contains(got.Body.String(), "把想推进的事放在这里") {
		t.Fatal(got.Code)
	}
	if strings.Contains(got.Body.String(), "<script>alert(1)</script>") {
		t.Fatal("unescaped title")
	}
	if f, _ := w.DB.GetFocus(); f.Revision != 0 {
		t.Fatal("GET seeded pins")
	}
}

func TestOverviewKeepsIndependentLineReachable(t *testing.T) {
	w := testThreadWeb(t)
	identityFile(t, w, "lines/solo.md", []byte("---\ntype: line\n---\n# 独立推进线入口\n"))
	got := requestThread(t, w, "GET", "/d/", nil, true)
	if got.Code != 200 || !strings.Contains(got.Body.String(), `href="/d/item/line:solo"`) {
		t.Fatal("standalone line lost from overview")
	}
}

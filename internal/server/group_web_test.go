package server

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// The dashboard form must create the same group the API would: owner as
// creator, opening message sent, mentioned members woken.
func TestDashboardCreateGroup(t *testing.T) {
	e := setup(t)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err := c.PostForm(e.ts.URL+"/d/login", url.Values{"key": {e.keys["owner"]}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	form := get(t, c, e.ts.URL+"/d/groups/new?item=english")
	for _, want := range []string{`name="topic"`, `value="assistant-cloud"`, `value="claude-code"`, `value="english" selected`, `name="at" value="all"`} {
		if !strings.Contains(form, want) {
			t.Fatalf("form missing %q", want)
		}
	}
	if strings.Contains(form, `name="member" value="owner"`) {
		t.Fatal("owner offered as a member of their own group")
	}
	csrf := regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`).FindStringSubmatch(form)[1]

	post := func(v url.Values) (*http.Response, string) {
		t.Helper()
		noRedirect := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		r, err := noRedirect.PostForm(e.ts.URL+"/d/groups", v)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r, string(b)
	}

	// Wrong CSRF is refused.
	if r, _ := post(url.Values{"csrf": {"bad"}, "item": {"english"}, "topic": {"x"}, "member": {"assistant-cloud"}}); r.StatusCode != 403 {
		t.Fatalf("bad csrf: %d", r.StatusCode)
	}
	// Missing member re-renders the form with the typed topic kept.
	r, body := post(url.Values{"csrf": {csrf}, "item": {"english"}, "topic": {"保留我"}})
	if r.StatusCode != 400 || !strings.Contains(body, `value="保留我"`) {
		t.Fatalf("missing member: %d", r.StatusCode)
	}
	// @ a non-member is rejected rather than waking a stranger.
	if r, _ := post(url.Values{"csrf": {csrf}, "item": {"english"}, "topic": {"x"}, "member": {"assistant-cloud"}, "at": {"claude-code"}}); r.StatusCode != 400 {
		t.Fatalf("stranger mention: %d", r.StatusCode)
	}

	r, _ = post(url.Values{"csrf": {csrf}, "item": {"english"}, "topic": {"网页建群"}, "member": {"assistant-cloud", "claude-code"},
		"body": {"请 Muse 先看"}, "at": {"assistant-cloud"}})
	if r.StatusCode != 303 || !strings.HasPrefix(r.Header.Get("Location"), "/d/groups/grp_") {
		t.Fatalf("create: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	gid := strings.TrimPrefix(r.Header.Get("Location"), "/d/groups/")

	code, g, _ := e.do(t, "muse", "GET", "/v1/groups/"+gid, nil)
	if code != 200 || g["created_by"] != "owner" || len(g["members"].([]any)) != 3 {
		t.Fatalf("group: %d %v", code, g)
	}
	_, _, msgs := e.do(t, "muse", "GET", "/v1/groups/"+gid+"/messages", nil)
	if len(msgs) != 1 {
		t.Fatalf("messages: %d", len(msgs))
	}
	m := msgs[0].(map[string]any)
	if m["from"] != "owner" || m["kind"] != "ask" || m["requested_by"] != "owner" {
		t.Fatalf("opening message: %v", m)
	}
	_, _, wk := e.do(t, "muse", "GET", "/v1/wakeups", nil)
	if len(wk) != 1 {
		t.Fatalf("muse wakeups: %d", len(wk))
	}
	_, _, wk = e.do(t, "claude-code", "GET", "/v1/wakeups", nil)
	if len(wk) != 0 {
		t.Fatalf("un-mentioned claude-code woken: %d", len(wk))
	}
	if page := get(t, c, e.ts.URL+"/d/groups/"+gid); !strings.Contains(page, "请 Muse 先看") {
		t.Fatal("detail page missing opening message")
	}
}

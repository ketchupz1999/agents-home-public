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

func TestGroupMemberChanges(t *testing.T) {
	e := setup(t)
	code, res, _ := e.do(t, "owner", "POST", "/v1/groups", map[string]any{
		"item": "english", "topic": "成员调整", "members": []string{"assistant-cloud"}, "body": "开始",
	})
	if code != 201 {
		t.Fatalf("create: %d %v", code, res)
	}
	gid := res["group"].(map[string]any)["id"].(string)

	// A mention creates a pending task we expect to be cancelled on removal.
	if code, _, _ = e.do(t, "owner", "POST", "/v1/groups/"+gid+"/messages", map[string]any{
		"body": "muse 你看", "mentions": []string{"assistant-cloud"},
	}); code != 201 {
		t.Fatalf("ask: %d", code)
	}
	_, _, wk := e.do(t, "muse", "GET", "/v1/wakeups", nil)
	if len(wk) != 1 {
		t.Fatalf("wakeups before: %d", len(wk))
	}

	// A non-member cannot change the roster.
	if code, _, _ = e.do(t, "limited", "POST", "/v1/groups/"+gid+"/members", map[string]any{"add": []string{"limited"}}); code != 404 {
		t.Fatalf("stranger membership change: %d", code)
	}
	// A member who is not the creator cannot either.
	if code, _, _ = e.do(t, "muse", "POST", "/v1/groups/"+gid+"/members", map[string]any{"add": []string{"claude-code"}}); code != 403 {
		t.Fatalf("non-creator membership change: %d", code)
	}
	// Nothing to do is a conflict, not a silent success.
	if code, _, _ = e.do(t, "owner", "POST", "/v1/groups/"+gid+"/members", map[string]any{}); code != 400 {
		t.Fatalf("empty change: %d", code)
	}

	// Add, then remove in one call: remove wins, so nothing is added.
	code, res, _ = e.do(t, "owner", "POST", "/v1/groups/"+gid+"/members", map[string]any{
		"add": []string{"claude-code"}, "remove": []string{"assistant-cloud"},
	})
	if code != 200 {
		t.Fatalf("change: %d %v", code, res)
	}
	if len(res["added"].([]any)) != 1 || len(res["removed"].([]any)) != 1 || res["cancelled_wakeups"].(float64) != 1 {
		t.Fatalf("change result: %v", res)
	}
	members := res["group"].(map[string]any)["members"].([]any)
	if len(members) != 2 {
		t.Fatalf("members: %v", members)
	}

	// The room now explains itself, and the removed agent can neither read nor
	// be handed a task in it.
	_, _, msgs := e.do(t, "claude-code", "GET", "/v1/groups/"+gid+"/messages", nil)
	if len(msgs) != 3 {
		t.Fatalf("messages after change: %d", len(msgs))
	}
	// Messages created in the same second sort by id, so find the note.
	found := false
	for _, raw := range msgs {
		if body, _ := raw.(map[string]any)["body"].(string); strings.Contains(body, "成员调整") {
			found = true
			if !strings.Contains(body, "加入：claude-code") || !strings.Contains(body, "移出：assistant-cloud") || !strings.Contains(body, "1 个待处理任务") {
				t.Fatalf("system note wording: %s", body)
			}
		}
	}
	if !found {
		t.Fatalf("no system note in %v", msgs)
	}
	if code, _, _ = e.do(t, "muse", "GET", "/v1/groups/"+gid+"/messages", nil); code != 404 {
		t.Fatalf("removed member can still read: %d", code)
	}
	if code, _, _ = e.do(t, "owner", "POST", "/v1/groups/"+gid+"/messages", map[string]any{
		"body": "再问", "mentions": []string{"assistant-cloud"},
	}); code != 400 {
		t.Fatalf("mention of removed member accepted: %d", code)
	}
	_, _, wk = e.do(t, "muse", "GET", "/v1/wakeups", nil)
	if len(wk) != 0 {
		t.Fatalf("removed member still has tasks: %v", wk)
	}

	// The new member can read the earlier history: it is one room.
	if code, _, _ = e.do(t, "claude-code", "GET", "/v1/groups/"+gid, nil); code != 200 {
		t.Fatalf("new member: %d", code)
	}

	// Re-adding an existing member is a conflict rather than a duplicate.
	if code, _, _ = e.do(t, "owner", "POST", "/v1/groups/"+gid+"/members", map[string]any{"add": []string{"claude-code"}}); code != 409 {
		t.Fatalf("no-op add: %d", code)
	}

	// A closed group is frozen.
	if code, _, _ = e.do(t, "owner", "POST", "/v1/groups/"+gid+"/close", nil); code != 200 {
		t.Fatalf("close: %d", code)
	}
	if code, _, _ = e.do(t, "owner", "POST", "/v1/groups/"+gid+"/members", map[string]any{"remove": []string{"claude-code"}}); code != 403 {
		t.Fatalf("closed group membership change: %d", code)
	}
}

// The dashboard form must route through the same rules as the API.
func TestDashboardGroupMembers(t *testing.T) {
	e := setup(t)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err := c.PostForm(e.ts.URL+"/d/login", url.Values{"key": {e.keys["owner"]}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	code, res, _ := e.do(t, "owner", "POST", "/v1/groups", map[string]any{
		"item": "english", "topic": "网页改成员", "members": []string{"assistant-cloud"}, "body": "开始",
	})
	if code != 201 {
		t.Fatalf("create: %d", code)
	}
	gid := res["group"].(map[string]any)["id"].(string)

	page := get(t, c, e.ts.URL+"/d/groups/"+gid)
	if !strings.Contains(page, "成员管理") || !strings.Contains(page, `name="add" value="claude-code"`) {
		t.Fatal("member management missing from group page")
	}
	if strings.Contains(page, `name="remove" value="owner"`) {
		t.Fatal("owner offered a self-removal")
	}
	csrf := regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`).FindAllStringSubmatch(page, -1)
	if len(csrf) == 0 {
		t.Fatal("no csrf on page")
	}
	token := csrf[len(csrf)-1][1]

	post := func(v url.Values) *http.Response {
		t.Helper()
		noRedirect := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		r, err := noRedirect.PostForm(e.ts.URL+"/d/groups/"+gid+"/members", v)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		return r
	}
	if r := post(url.Values{"csrf": {"bad"}, "add": {"claude-code"}}); r.StatusCode != 403 {
		t.Fatalf("bad csrf: %d", r.StatusCode)
	}
	if r := post(url.Values{"csrf": {token}, "add": {"claude-code"}, "remove": {"assistant-cloud"}}); r.StatusCode != 303 {
		t.Fatalf("change: %d", r.StatusCode)
	}
	_, g, _ := e.do(t, "claude-code", "GET", "/v1/groups/"+gid, nil)
	members := g["members"].([]any)
	if len(members) != 2 || members[0] != "claude-code" || members[1] != "owner" {
		t.Fatalf("members after dashboard change: %v", members)
	}
}

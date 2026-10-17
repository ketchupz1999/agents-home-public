package web

import (
	"fmt"
	"html"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ketchupz1999/agents-home-public/internal/db"
	"github.com/ketchupz1999/agents-home-public/internal/protocol"
)

func testTeamWeb(t *testing.T) *Web {
	w := testThreadWeb(t)
	w.Now = func() time.Time { return time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC) }
	for id, name := range map[string]string{"my-workspace": "MyWorkspace", "assistant-cloud": "Muse", "my-finance-studio": "MyFinanceStudio"} {
		identityFile(t, w, "agents/"+id+"/card.md", []byte("---\ndisplay_name: "+name+"\n---\n"))
	}
	identityFile(t, w, "agents/registry.yaml", []byte(`roles:
- id: assistant-cloud
  character: muse
- id: my-workspace
  workspace: ~/agents-workspace
  character: codex
  invocation: on_demand
- id: my-finance-studio
  character: placeholder
agents:
- id: assistant-cloud
  role_id: assistant-cloud
  agent: Muse
- id: workmac-codex
  role_id: my-workspace
  agent: Codex
- id: workmac-claude
  role_id: my-workspace
  agent: Claude Code
`))
	return w
}
func teamMessage(id, from, to string) protocol.Message {
	return protocol.Message{ID: id, From: from, To: to, Item: "agent-hub", Kind: "ask", Task: "讨论 " + id, Body: "正文", CreatedAt: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)}
}
func TestTeamGroupingKeepsPrincipalsAndFrozenMessageRole(t *testing.T) {
	w := testTeamWeb(t)
	msgs := []protocol.Message{teamMessage("first", "workmac-codex", "assistant-cloud"), teamMessage("internal", "workmac-claude", "workmac-codex"), teamMessage("owner-msg", "owner", "workmac-codex"), teamMessage("unknown", "unmapped-agent", "assistant-cloud")}
	former := teamMessage("former", "workmac-codex", "assistant-cloud")
	former.FromRole = "previous-role"
	msgs = append(msgs, former)
	for _, m := range msgs {
		putThread(t, w, m)
	}
	// Identities known only from old messages are folded away by default.
	folded, err := w.loadTeam(httptest.NewRequest("GET", "/d/agents", nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(folded.edges()) != 1 || len(folded.shownNodes()) != 3 {
		t.Fatalf("folded edges=%d nodes=%d", len(folded.edges()), len(folded.shownNodes()))
	}
	snap, err := w.loadTeam(httptest.NewRequest("GET", "/d/agents?history=1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.edges()) != 3 {
		t.Fatalf("edges: %+v", snap.edges())
	}
	n := snap.ByKey["r:my-workspace"]
	if n == nil || len(n.Principals) != 2 {
		t.Fatal("principal grouping lost")
	}
	s := snap.stats(n, false)
	if s.Participated != 2 || s.Started != 2 || s.Partners != 1 {
		t.Fatal(s)
	}
	if snap.ByKey["p:unmapped-agent"] == nil || snap.ByKey["r:previous-role"] == nil {
		t.Fatal("unknown/frozen identity dropped")
	}
	for _, route := range []string{"/d/agents", "/d/agents/my-workspace", "/d/agents/my-finance-studio?tab=runtime"} {
		res := requestThread(t, w, "GET", route, nil, true)
		if res.Code != 200 {
			t.Fatal(route, res.Code, res.Body.String())
		}
	}
	// A message whose frozen role is a role that no longer exists keeps its
	// own node; the squad map must not rewrite it to the sender's current role.
	if snap.ByKey["r:previous-role"] == nil {
		t.Fatal("historical role folded into the current role")
	}
	if n := snap.ByKey["r:my-workspace"]; n == nil || len(n.Principals) != 2 {
		t.Fatal("current role lost its principals")
	}
	for _, m := range msgs {
		if w.DB.IsRead("owner", m.ID) {
			t.Fatal("GET changed read receipts")
		}
		saved, _ := w.DB.GetMessage(m.ID)
		if saved.From != m.From || saved.To != m.To {
			t.Fatal("principal rewritten")
		}
	}
}
func TestTeamUnconnectedRoleNeverQueriesAllRuns(t *testing.T) {
	w := testTeamWeb(t)
	_, err := w.DB.RegisterRun(db.Run{PrincipalID: "workmac-codex", ClientRunID: "private_run_123", Harness: "PRIVATE-HARNESS"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/d/agents/my-finance-studio?tab=runtime", "/d/agents/my-finance-studio?tab=activity&type=reads", "/d/agents/my-finance-studio?tab=data"} {
		got := requestThread(t, w, "GET", path, nil, true)
		if got.Code != 200 || strings.Contains(got.Body.String(), "PRIVATE-HARNESS") {
			t.Fatal("empty principals became all", path)
		}
	}
}
func TestTeamPublishedMetadataPermissionsAndAuth(t *testing.T) {
	w := testTeamWeb(t)
	w.AccessForIdentity = func(id string) (IdentityAccess, bool) {
		switch id {
		case "workmac-codex":
			return IdentityAccess{ID: id, Read: []string{"profile/"}}, true
		case "workmac-claude":
			return IdentityAccess{ID: id, Read: []string{"items/"}}, true
		}
		return IdentityAccess{}, false
	}
	// 工作树修改未发布，不能改变名片或角色归属。
	if err := os.WriteFile(filepath.Join(w.Store.Dir, "agents/my-workspace/card.md"), []byte("---\ndisplay_name: UNPUBLISHED\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	res := requestThread(t, w, "GET", "/d/agents/my-workspace?tab=data", nil, true)
	body := res.Body.String()
	for _, s := range []string{"MyWorkspace", "workmac-codex", "workmac-claude", "profile/", "items/"} {
		if !strings.Contains(body, s) {
			t.Fatal("missing", s)
		}
	}
	if strings.Contains(body, "UNPUBLISHED") || strings.Contains(body, string(w.SecretKey)) {
		t.Fatal("unpublished or secret output")
	}
	for _, path := range []string{"/d/agents", "/d/agents/my-workspace?tab=meta"} {
		if got := requestThread(t, w, "GET", path, nil, false); got.Code != 303 {
			t.Fatal("unauthenticated dashboard", path)
		}
	}
}
func TestTeamMessagePagingSnapshotAndDirection(t *testing.T) {
	w := testTeamWeb(t)
	for i := 0; i < 101; i++ {
		putThread(t, w, teamMessage(fmt.Sprintf("m-%03d", i), "workmac-codex", "assistant-cloud"))
	}
	res := requestThread(t, w, "GET", "/d/agents/my-workspace?type=mail&direction=sent", nil, true)
	if strings.Count(res.Body.String(), `class="team-event"`) != 100 {
		t.Fatal("page size")
	}
	matches := regexp.MustCompile(`<a class="team-next" href="([^"]+)"`).FindStringSubmatch(res.Body.String())
	if len(matches) != 2 {
		t.Fatal("missing cursor")
	}
	later := teamMessage("later", "workmac-codex", "assistant-cloud")
	later.CreatedAt = w.Now().Add(time.Second)
	putThread(t, w, later)
	second := requestThread(t, w, "GET", html.UnescapeString(matches[1]), nil, true)
	if strings.Count(second.Body.String(), `class="team-event"`) != 1 || strings.Contains(second.Body.String(), "讨论 later") {
		t.Fatal("unstable paging", second.Code)
	}
	received := requestThread(t, w, "GET", "/d/agents/my-workspace?type=mail&direction=received", nil, true)
	if strings.Count(received.Body.String(), `class="team-event"`) != 0 {
		t.Fatal("direction ignored")
	}
}
func TestTeamDataFailuresNeverRenderZeroStats(t *testing.T) {
	w := testTeamWeb(t)
	w.DB.Close()
	res := requestThread(t, w, "GET", "/d/agents", nil, true)
	if res.Code != 200 || !strings.Contains(res.Body.String(), "信箱暂时无法读取") || strings.Contains(res.Body.String(), "新话题 0") {
		t.Fatal("DB failure represented as zero")
	}
}
func TestTeamObservationCursorAndEmptyContextHash(t *testing.T) {
	w := testTeamWeb(t)
	at := w.Now().Add(-time.Hour).Format(db.TimestampFormat)
	for i := 0; i < 101; i++ {
		if err := w.DB.RecordObservation(db.Observation{PrincipalID: "workmac-codex", Kind: "file.served", OccurredAt: at, Resource: "items/example/CONTEXT.md", ContentSHA: "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391", MetadataJSON: `{"endpoint":"item"}`}); err != nil {
			t.Fatal(err)
		}
	}
	res := requestThread(t, w, "GET", "/d/agents/my-workspace?type=reads", nil, true)
	if strings.Count(res.Body.String(), `class="team-event"`) != 100 {
		t.Fatal("observation page size")
	}
	matches := regexp.MustCompile(`<a class="team-next" href="([^"]+)"`).FindStringSubmatch(res.Body.String())
	if len(matches) != 2 {
		t.Fatal("missing next")
	}
	u, err := url.Parse(html.UnescapeString(matches[1]))
	if err != nil {
		t.Fatal(err)
	}
	second := requestThread(t, w, "GET", u.String(), nil, true)
	if strings.Count(second.Body.String(), `class="team-event"`) != 1 {
		t.Fatal("observation paging lost tie")
	}
}

func TestRunSnapshotSurvivesNewRequestsAfterCutoff(t *testing.T) {
	w := testTeamWeb(t)
	cut := w.Now().Add(-time.Hour)
	_, err := w.DB.RegisterRun(db.Run{PrincipalID: "workmac-codex", ClientRunID: "stable_run_001", Harness: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.DB.Exec(`UPDATE agent_runs SET first_seen_at=?`, cut.Add(-time.Hour).Format(db.TimestampFormat)); err != nil {
		t.Fatal(err)
	}
	before := cut.Add(-time.Minute).Format(db.TimestampFormat)
	if err = w.DB.RecordObservation(db.Observation{PrincipalID: "workmac-codex", RunID: "stable_run_001", Kind: "inbox.listed", OccurredAt: before, Resource: "inbox", MetadataJSON: `{}`}); err != nil {
		t.Fatal(err)
	}
	path := "/d/agents/my-workspace?tab=runtime&at=" + url.QueryEscape(cut.Format(time.RFC3339Nano))
	first := requestThread(t, w, "GET", path, nil, true)
	if !strings.Contains(first.Body.String(), "stable_run_001") {
		t.Fatal("initial run missing")
	}
	if err = w.DB.RecordObservation(db.Observation{PrincipalID: "workmac-codex", RunID: "stable_run_001", Kind: "inbox.listed", OccurredAt: cut.Add(time.Minute).Format(db.TimestampFormat), Resource: "inbox", MetadataJSON: `{}`}); err != nil {
		t.Fatal(err)
	}
	second := requestThread(t, w, "GET", path, nil, true)
	if first.Body.String() != second.Body.String() {
		t.Fatal("run historical snapshot changed after future request")
	}
}
func TestDataMapFailureProducesUnknownEdges(t *testing.T) {
	w := testTeamWeb(t)
	if _, err := w.DB.Exec(`DROP TABLE agent_observations`); err != nil {
		t.Fatal(err)
	}
	body := requestThread(t, w, "GET", "/d/agents?view=data", nil, true).Body.String()
	if !strings.Contains(body, `class="unknown" data-edge=`) || !strings.Contains(body, "交互未知") || strings.Contains(body, `class="dashed" data-edge=`) {
		t.Fatal("missing evidence collapsed to registration-only")
	}
}

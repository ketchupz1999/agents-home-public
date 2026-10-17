package web

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ketchupz1999/agents-home-public/internal/protocol"
)

// Mirrors production: muse was once keyed assistant-cloud and its messages
// carry from_role=assistant-cloud; groups must not appear as teammates.
func TestTeamFoldsHistoryMergesAliasesAndSkipsGroups(t *testing.T) {
	w := testThreadWeb(t)
	w.Now = func() time.Time { return time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC) }
	identityFile(t, w, "agents/muse/card.md", []byte("---\ndisplay_name: 狗腿子（Muse）\n---\n"))
	identityFile(t, w, "agents/my-workspace/card.md", []byte("---\ndisplay_name: MyAgentSpace\n---\n"))
	identityFile(t, w, "agents/registry.yaml", []byte(`roles:
- id: muse
- id: my-workspace
agents:
- id: muse
  role_id: muse
  aliases: [assistant-cloud]
- id: work-agent
  role_id: my-workspace
  aliases: [workmac-codex]
`))
	at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	msg := func(id, from, role, to, group string) protocol.Message {
		return protocol.Message{ID: id, From: from, FromRole: role, To: to, GroupID: group, Item: "agent-hub", Kind: "ask", Task: id, Body: "x", CreatedAt: at}
	}
	for _, m := range []protocol.Message{
		msg("a", "assistant-cloud", "assistant-cloud", "workmac-codex", ""),
		msg("b", "assistant-cloud", "", "workmac-codex", ""),
		msg("c", "muse", "muse", "work-agent", ""),
		msg("d", "workmac-codex", "my-workspace", "assistant-cloud", ""),
		msg("e", "claude-local", "", "assistant-cloud", ""),
		msg("f", "muse", "muse", "group:grp_1", "grp_1"),
	} {
		putThread(t, w, m)
	}
	snap, err := w.loadTeam(httptest.NewRequest("GET", "/d/agents", nil))
	if err != nil {
		t.Fatal(err)
	}
	muse := 0
	for _, n := range snap.Nodes {
		if strings.HasPrefix(n.ID, "group:") || strings.HasPrefix(n.ID, "grp_") {
			t.Fatalf("group became a node: %s", n.Key)
		}
		if n.Identity.Name == "狗腿子（Muse）" {
			muse++
		}
	}
	if muse != 1 {
		t.Fatalf("muse drawn %d times: %+v", muse, snap.Nodes)
	}
	e := snap.edges()
	if len(e) != 1 || len(e[0].Messages) != 4 {
		t.Fatalf("aliases not merged into one edge: %+v", e)
	}
	if snap.ByKey["p:claude-local"] == nil || len(snap.shownNodes()) != 2 {
		t.Fatalf("deleted identity should exist but be folded; shown=%d", len(snap.shownNodes()))
	}
	body := requestThread(t, w, "GET", "/d/agents", nil, true).Body.String()
	if strings.Contains(body, `data-node="p:claude-local"`) || !strings.Contains(body, "显示历史身份（1）") {
		t.Fatal("history not folded by default")
	}
	body = requestThread(t, w, "GET", "/d/agents?history=1", nil, true).Body.String()
	if !strings.Contains(body, `data-node="p:claude-local"`) || !strings.Contains(body, "隐藏历史身份") {
		t.Fatal("history toggle does not reveal")
	}
}

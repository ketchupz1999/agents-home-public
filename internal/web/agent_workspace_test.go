package web

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ketchupz1999/agents-home-public/internal/agents"
	"github.com/ketchupz1999/agents-home-public/internal/db"
	"github.com/ketchupz1999/agents-home-public/internal/protocol"
	"gopkg.in/yaml.v3"
)

func TestWorkspaceMultipleMachinesRetainOneRoleAndRuntimeSource(t *testing.T) {
	w := testTeamWeb(t)
	reg, err := agents.Load(w.Store)
	if err != nil {
		t.Fatal(err)
	}
	reg.Agents = append(reg.Agents, agents.Principal{ID: "home-codex", RoleID: "my-workspace", Agent: "Codex"})
	for i := range reg.Roles {
		if reg.Roles[i].ID != "my-workspace" {
			continue
		}
		reg.Roles[i].WorkspaceDescription = "个人事项 <script>unsafe()</script>"
		reg.Roles[i].Repository = agents.Repository{Status: "git", URL: "https://github.com/example/agent"}
		reg.Roles[i].Locations = []agents.Location{{DeviceID: "workmac", DeviceName: "工作 Mac", Path: "~/agents-workspace", Hosting: "self_managed"}, {DeviceID: "home-example", DeviceName: "家中测试机", Path: "/home/test/agent", Hosting: "self_managed"}}
		reg.Roles[i].Skills = []string{"aspace-data", "aspace-mailbox", "aspace-ops"}
	}
	raw, err := yaml.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	identityFile(t, w, "agents/registry.yaml", raw)
	identityFile(t, w, "agents/my-workspace/card.md", []byte("---\ndisplay_name: MyAgentSpace\n---\n"))
	snap, err := w.loadTeam(httptest.NewRequest("GET", "/d/agents", nil))
	if err != nil || len(snap.Nodes) != 3 || len(snap.ByKey["r:my-workspace"].Principals) != 3 {
		t.Fatalf("machine became a role: %v %v", snap, err)
	}
	body := requestThread(t, w, "GET", "/d/agents/my-workspace?tab=workspace", nil, true).Body.String()
	for _, s := range []string{"MyAgentSpace", "工作 Mac", "家中测试机", "/home/test/agent", "https://github.com/example/agent", "aspace-mailbox/SKILL.md", "&lt;script&gt;"} {
		if !strings.Contains(body, s) {
			t.Fatal("missing", s)
		}
	}
	if strings.Contains(body, "<script>unsafe()") {
		t.Fatal("unescaped workspace metadata")
	}
	if got := requestThread(t, w, "GET", "/d/agents/my-workspace?tab=workspace", nil, false); got.Code != 303 {
		t.Fatal("unauthenticated workspace", got.Code)
	}
	for _, run := range []db.Run{{PrincipalID: "workmac-codex", ClientRunID: "same_run_001", DeviceID: "workmac", Harness: "codex", Workspace: "~/agents-workspace"}, {PrincipalID: "home-codex", ClientRunID: "same_run_001", DeviceID: "home-example", Harness: "claude", Workspace: "/home/test/runtime"}} {
		if _, err := w.DB.RegisterRun(run); err != nil {
			t.Fatal(err)
		}
		if err := w.DB.RecordObservation(db.Observation{PrincipalID: run.PrincipalID, RunID: run.ClientRunID, Kind: "file.served", Resource: "items/agent-hub/CONTEXT.md", OccurredAt: w.Now().Add(-time.Hour).Format(db.TimestampFormat), MetadataJSON: `{}`}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.DB.Exec(`UPDATE agent_runs SET first_seen_at=?`, w.Now().Add(-2*time.Hour).Format(db.TimestampFormat)); err != nil {
		t.Fatal(err)
	}
	runtime := requestThread(t, w, "GET", "/d/agents/my-workspace?tab=runtime", nil, true).Body.String()
	if strings.Count(runtime, `class="team-run"`) != 2 || !strings.Contains(runtime, "home-example · claude") || !strings.Contains(runtime, "workmac · codex") {
		t.Fatal("lost machine runtimes")
	}
	if !strings.Contains(runtime, `id="run-home-codex/same_run_001"`) || !strings.Contains(runtime, `id="run-workmac-codex/same_run_001"`) {
		t.Fatal("run IDs collide across principals")
	}
	reads := requestThread(t, w, "GET", "/d/agents/my-workspace?type=reads", nil, true).Body.String()
	if !strings.Contains(reads, "/home/test/runtime") || !strings.Contains(reads, "home-example") {
		t.Fatal("observation source not joined by identity")
	}
	m := teamMessage("home-mail", "home-codex", "assistant-cloud")
	m.Runtime = &protocol.Runtime{RunID: "same_run_001", Harness: "claude", DeviceID: "home-example", Workspace: "/home/test/runtime"}
	putThread(t, w, m)
	mail := requestThread(t, w, "GET", "/d/agents/my-workspace?type=mail", nil, true).Body.String()
	if !strings.Contains(mail, "home-example · claude · run same_run_001") {
		t.Fatal("mail source lost")
	}
}

func TestHostedWorkspaceWithoutGitOrRuntime(t *testing.T) {
	w := testTeamWeb(t)
	reg, err := agents.Load(w.Store)
	if err != nil {
		t.Fatal(err)
	}
	for i := range reg.Roles {
		if reg.Roles[i].ID == "my-finance-studio" {
			reg.Roles[i].Repository = agents.Repository{Status: "none"}
			reg.Roles[i].Locations = []agents.Location{{Hosting: "platform_managed"}}
		}
	}
	raw, err := yaml.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	identityFile(t, w, "agents/registry.yaml", raw)
	body := requestThread(t, w, "GET", "/d/agents/my-finance-studio?tab=workspace", nil, true).Body.String()
	for _, s := range []string{"尚未接入", "无独立 Git 仓库", "平台托管", "设备未登记"} {
		if !strings.Contains(body, s) {
			t.Fatal("missing", s)
		}
	}
	if strings.Contains(body, `class="workspace-repo-link"`) {
		t.Fatal("invented repository link")
	}
}

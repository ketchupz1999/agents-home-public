package web

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ketchupz1999/agents-home-public/internal/db"
)

func TestActionBoardDayAndWeekBoundaries(t *testing.T) {
	for _, tc := range []struct{ at, kind, date, want string }{
		{"2026-10-13T15:59:59Z", "day", "2026-10-14", "week"},
		{"2026-10-13T16:00:00Z", "day", "2026-10-14", "today"},
		{"2026-10-13T16:00:00Z", "day", "2026-10-13", "overdue"},
		{"2026-10-18T15:59:59Z", "week", "2026-10-12", "week"},
		{"2026-10-18T16:00:00Z", "week", "2026-10-12", "overdue"},
		{"2026-10-18T16:00:00Z", "week", "2026-10-19", "week"},
		{"2026-10-13T16:00:00Z", "day", "2026-10-20", "future"},
		{"2026-10-13T16:00:00Z", "later", "", "later"},
	} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		a := db.Action{ActionProposal: db.ActionProposal{PlanKind: tc.kind, PlanDate: tc.date}, Status: "planned"}
		if got := actionBucket(a, at); got != tc.want {
			t.Fatalf("%+v: %s", tc, got)
		}
	}
}

func TestActionOwnerFormsPersistenceAndConflict(t *testing.T) {
	w := testTeamWeb(t)
	w.Now = func() time.Time { return time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC) }
	identityFile(t, w, "items/agent-hub/CONTEXT.md", []byte("# Agent Hub\n"))
	a, _, err := w.DB.CreateAction(db.ActionProposal{ClientID: "web-case", Item: "agent-hub", Title: "<script>alert(1)</script>", Details: "一件真实的行动", PlanKind: "later", SourcePath: "items/agent-hub/CONTEXT.md"}, db.ActionActor{Name: "workmac-codex", Role: "my-workspace"}, "fixture-sha")
	if err != nil {
		t.Fatal(err)
	}
	form := func(rev string, when string) url.Values {
		return url.Values{"csrf": {w.actionCSRF()}, "revision": {rev}, "when": {when}}
	}
	path := "/d/actions/" + a.ID + "/"
	if got := requestThread(t, w, "POST", path+"schedule", form("1", "today"), false); got.Code != 303 {
		t.Fatal(got.Code)
	}
	bad := form("1", "today")
	bad.Set("csrf", w.focusCSRF())
	if got := requestThread(t, w, "POST", path+"schedule", bad, true); got.Code != 403 {
		t.Fatal("wrong csrf", got.Code)
	}
	if got := requestThread(t, w, "POST", path+"complete", form("1", ""), true); got.Code != 400 {
		t.Fatal("completed unaccepted suggestion", got.Code)
	}
	if got := requestThread(t, w, "POST", path+"schedule", form("1", "today"), true); got.Code != 303 {
		t.Fatal(got.Code, got.Body.String())
	}
	body := requestThread(t, w, "GET", "/d/", nil, true).Body.String()
	if !strings.Contains(body, `class="action-card action-today"`) || strings.Contains(body, "<script>alert(1)</script>") || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("today render/escaping")
	}
	if got := requestThread(t, w, "POST", path+"schedule", form("1", "later"), true); got.Code != 409 {
		t.Fatal("stale overwrite", got.Code)
	}
	if got := requestThread(t, w, "POST", path+"complete", form("2", ""), true); got.Code != 303 {
		t.Fatal(got.Code)
	}
	if got := requestThread(t, w, "POST", path+"complete", form("2", ""), true); got.Code != 303 {
		t.Fatal("repeat complete", got.Code)
	}
	history, err := w.DB.ActionHistory(a.ID)
	if err != nil || len(history) != 3 {
		t.Fatal("audit duplicate", len(history), err)
	}
	if err := w.DB.Close(); err != nil {
		t.Fatal(err)
	}
	w.DB, err = db.Open(filepath.Join(w.Store.Dir, "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.DB.Close() })
	saved, err := w.DB.GetAction(a.ID)
	if err != nil || saved.Status != "done" || saved.Revision != 3 || saved.CompletedAt == "" {
		t.Fatal("restart lost action", saved, err)
	}
	if got := requestThread(t, w, "POST", path+"reopen", form("3", ""), true); got.Code != 303 {
		t.Fatal(got.Code)
	}
	v := form("4", "date")
	v.Set("date", "2026-10-21")
	if got := requestThread(t, w, "POST", path+"schedule", v, true); got.Code != 303 {
		t.Fatal(got.Code)
	}
	saved, _ = w.DB.GetAction(a.ID)
	if saved.PlanDate != "2026-10-21" || saved.Status != "planned" || saved.CompletedAt != "" {
		t.Fatal(saved)
	}
}

func TestActionBoardReadFailureIsNotEmpty(t *testing.T) {
	w := testTeamWeb(t)
	if _, err := w.DB.Exec(`DROP TABLE actions`); err != nil {
		t.Fatal(err)
	}
	body := requestThread(t, w, "GET", "/d/", nil, true).Body.String()
	if !strings.Contains(body, "行动清单暂时无法读取") || strings.Contains(body, "今天还没有安排") {
		t.Fatal("storage failure became empty board")
	}
}

func TestOwnerCanReachActionsBeyondTwenty(t *testing.T) {
	w := testTeamWeb(t)
	identityFile(t, w, "items/agent-hub/CONTEXT.md", []byte("# Agent Hub\n"))
	ids := []string{}
	for i := 0; i < 21; i++ {
		a, _, err := w.DB.CreateAction(db.ActionProposal{ClientID: fmt.Sprint(i), Item: "agent-hub", Title: fmt.Sprintf("行动 %d", i), PlanKind: "later", SourcePath: "items/agent-hub/CONTEXT.md"}, db.ActionActor{Name: "workmac-codex"}, "sha")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.DB.UpdateAction(a.ID, db.ActionActor{Name: "owner", Owner: true}, db.ActionUpdate{Operation: "schedule", Revision: 1, PlanKind: "later"}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	for _, state := range []string{"planned", "done"} {
		if state == "done" {
			for _, id := range ids {
				if _, err := w.DB.UpdateAction(id, db.ActionActor{Name: "owner", Owner: true}, db.ActionUpdate{Operation: "complete", Revision: 2}); err != nil {
					t.Fatal(err)
				}
			}
		}
		body := requestThread(t, w, "GET", "/d/", nil, true).Body.String()
		for _, id := range ids {
			if !strings.Contains(body, `data-action-id="`+id+`"`) {
				t.Fatal("owner lost action", state, id)
			}
			op := "complete"
			if state == "done" {
				op = "reopen"
			}
			if !strings.Contains(body, `action="/d/actions/`+id+`/`+op+`"`) {
				t.Fatal("owner lost operation", state, id)
			}
		}
	}
}

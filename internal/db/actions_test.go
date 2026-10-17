package db

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func actionDB(t *testing.T) *DB {
	t.Helper()
	d, e := Open(filepath.Join(t.TempDir(), "a.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { d.Close() })
	return d
}
func proposal() ActionProposal {
	return ActionProposal{ClientID: "retry-1", Item: "english", Title: "practice", PlanKind: "later", SourcePath: "items/english/CONTEXT.md"}
}

var ownerAction = ActionActor{Name: "owner", Owner: true}
var agentAction = ActionActor{Name: "muse"}

func TestActionsLifecycleRetryAndCAS(t *testing.T) {
	d := actionDB(t)
	p := proposal()
	a, created, err := d.CreateAction(p, agentAction, "sha")
	if err != nil || !created {
		t.Fatal(a, created, err)
	}
	if _, err = d.UpdateAction(a.ID, agentAction, ActionUpdate{Operation: "schedule", Revision: 1, PlanKind: "later"}); !errors.Is(err, ErrActionForbidden) {
		t.Fatal(err)
	}
	if _, err = d.UpdateAction(a.ID, ownerAction, ActionUpdate{Operation: "complete", Revision: 1}); !errors.Is(err, ErrActionValidation) {
		t.Fatal(err)
	}
	a, err = d.UpdateAction(a.ID, ownerAction, ActionUpdate{Operation: "schedule", Revision: 1, PlanKind: "day", PlanDate: "2026-10-14"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.UpdateAction(a.ID, ownerAction, ActionUpdate{Operation: "schedule", Revision: 1, PlanKind: "later"}); !errors.Is(err, ErrActionConflict) {
		t.Fatal(err)
	}
	a, err = d.UpdateAction(a.ID, ownerAction, ActionUpdate{Operation: "complete", Revision: 2})
	if err != nil {
		t.Fatal(err)
	}
	retry, created, err := d.CreateAction(p, agentAction, "new-source-sha")
	if err != nil || created || retry.Status != "done" || retry.Revision != 3 || retry.SourceSHA != "sha" {
		t.Fatal(retry, created, err)
	}
	if _, err = d.UpdateAction(a.ID, ownerAction, ActionUpdate{Operation: "complete", Revision: 2}); err != nil {
		t.Fatal(err)
	}
	h, err := d.ActionHistory(a.ID)
	if err != nil || len(h) != 3 {
		t.Fatal(h, err)
	}
	p.Title = "changed"
	if _, _, err = d.CreateAction(p, agentAction, "sha"); !errors.Is(err, ErrActionConflict) {
		t.Fatal(err)
	}
	a, err = d.UpdateAction(a.ID, ownerAction, ActionUpdate{Operation: "reopen", Revision: 3})
	if err != nil || a.PlanDate != "2026-10-14" || a.CompletedAt != "" {
		t.Fatal(a, err)
	}
	if _, err = d.UpdateAction(a.ID, ownerAction, ActionUpdate{Operation: "dismiss", Revision: 4}); !errors.Is(err, ErrActionValidation) {
		t.Fatal(err)
	}
}

func TestActionAuditRollbackAndConcurrentUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	a, _, err := d.CreateAction(proposal(), agentAction, "sha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`CREATE TRIGGER fail_action_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = d.UpdateAction(a.ID, ownerAction, ActionUpdate{Operation: "schedule", Revision: 1, PlanKind: "later"}); err == nil {
		t.Fatal("audit failure accepted")
	}
	got, _ := d.GetAction(a.ID)
	if got.Status != "suggested" || got.Revision != 1 {
		t.Fatal(got)
	}
	p := proposal()
	p.ClientID = "new"
	if _, _, err = d.CreateAction(p, agentAction, "sha"); err == nil {
		t.Fatal("creation survived audit failure")
	}
	all, _ := d.ListActions("", "")
	if len(all) != 1 {
		t.Fatal(all)
	}
	d.Exec(`DROP TRIGGER fail_action_audit`)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i, date := range []string{"2026-10-14", "2026-10-15"} {
		client := d
		if i == 1 {
			client = other
		}
		wg.Add(1)
		go func(client *DB, date string) {
			defer wg.Done()
			_, e := client.UpdateAction(a.ID, ownerAction, ActionUpdate{Operation: "schedule", Revision: 1, PlanKind: "day", PlanDate: date})
			errs <- e
		}(client, date)
	}
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for e := range errs {
		if e == nil {
			success++
		} else if errors.Is(e, ErrActionConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	h, _ := d.ActionHistory(a.ID)
	if len(h) != 2 {
		t.Fatal(h)
	}
}

func TestActionConcurrentCreateAndWeek(t *testing.T) {
	if ValidateActionPlan("week", "2026-10-14") == nil || ValidateActionPlan("week", "2026-10-12") != nil {
		t.Fatal("week validation")
	}
	d := actionDB(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, e := d.CreateAction(proposal(), agentAction, "sha"); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	all, _ := d.ListActions("", "")
	if len(all) != 1 {
		t.Fatal(all)
	}
	h, _ := d.ActionHistory(all[0].ID)
	if len(h) != 1 {
		t.Fatal(h)
	}
}

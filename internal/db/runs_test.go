package db

import (
	"errors"
	"github.com/ketchupz1999/agents-home-public/internal/protocol"
	"path/filepath"
	"testing"
	"time"
)

func TestRunsTransactionsAndPagination(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	r := Run{PrincipalID: "a", ClientRunID: "test_run_1", Harness: "codex"}
	first, err := d.RegisterRun(r)
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.RegisterRun(r)
	if err != nil || first.FirstSeenAt != second.FirstSeenAt {
		t.Fatal("retry not idempotent", err)
	}
	r.Harness = "claude"
	if _, err = d.RegisterRun(r); !errors.Is(err, ErrRunConflict) {
		t.Fatal(err)
	}
	if _, err = d.GetRun("b", r.ClientRunID); !errors.Is(err, ErrRunNotFound) {
		t.Fatal(err)
	}
	o := Observation{PrincipalID: "a", RunID: r.ClientRunID, Kind: "file.served", Resource: "a.md", MetadataJSON: `{"endpoint":"file"}`, OccurredAt: "2026-10-13T01:00:00Z"}
	for i := 0; i < 3; i++ {
		if err = d.RecordObservation(o); err != nil {
			t.Fatal(err)
		}
	}
	page, err := d.ListObservations(ObservationFilter{Principals: []string{"a"}, Limit: 2})
	if err != nil || len(page) != 2 {
		t.Fatal(page, err)
	}
	next, err := d.ListObservations(ObservationFilter{BeforeAt: page[1].OccurredAt, BeforeID: page[1].ID, Limit: 2})
	if err != nil || len(next) != 1 || next[0].ID >= page[1].ID {
		t.Fatal(next, err)
	}
	o.RunID = "missing_123"
	if err = d.RecordObservation(o); !errors.Is(err, ErrRunNotFound) {
		t.Fatal(err)
	}
	var count int
	d.QueryRow(`SELECT count(*) FROM agent_observations`).Scan(&count)
	if count != 3 {
		t.Fatal("failed observation committed")
	}
	m := protocol.Message{ID: "msg", From: "a", CreatedAt: time.Now()}
	if err = d.PutMessageWithRun(m, "missing_123"); !errors.Is(err, ErrRunNotFound) {
		t.Fatal(err)
	}
	saved, err := d.GetMessage("msg")
	if err != nil || saved != nil {
		t.Fatal("failed message committed")
	}
	if err = d.PutMessageWithRun(m, r.ClientRunID); err != nil {
		t.Fatal(err)
	}
	run, err := d.GetRun("a", r.ClientRunID)
	if err != nil || run.LastSeenAt == "" {
		t.Fatal(run, err)
	}
}

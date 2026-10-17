package db

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestFocusEmptyRetryConflictAndCorruption(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "focus.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	f, err := d.GetFocus()
	if err != nil || f.Revision != 0 {
		t.Fatal(f, err)
	}
	f, err = d.SaveFocus(nil, 0)
	if err != nil || f.Revision != 1 {
		t.Fatal("first empty must persist", f, err)
	}
	f, err = d.SaveFocus([]string{}, 0)
	if err != nil || f.Revision != 1 {
		t.Fatal("empty retry", f, err)
	}
	f, err = d.SaveFocus([]string{"english", "agent-hub"}, 1)
	if err != nil || f.Revision != 2 || f.Items[0] != "english" {
		t.Fatal(f, err)
	}
	f, err = d.SaveFocus([]string{"english", "agent-hub"}, 1)
	if err != nil || f.Revision != 2 {
		t.Fatal("retry", f, err)
	}
	_, err = d.SaveFocus([]string{"agent-hub", "english"}, 1)
	if !errors.Is(err, ErrFocusConflict) {
		t.Fatal("stale order overwrite", err)
	}
	for _, items := range [][]string{{"a", "a"}, {"a", "b", "c"}, {"../x"}, {""}} {
		if _, err = d.SaveFocus(items, 2); err == nil {
			t.Fatal("invalid accepted", items)
		}
	}
	if _, err = d.Exec(`UPDATE focus SET items='null'`); err != nil {
		t.Fatal(err)
	}
	if _, err = d.GetFocus(); err == nil {
		t.Fatal("corrupt data treated as empty")
	}
	if _, err = d.SaveFocus(nil, 2); err == nil {
		t.Fatal("corrupt data overwritten")
	}
}
func TestFocusConcurrentConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "focus.db")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, base := range []int64{0, 1} {
		start := make(chan struct{})
		results := make(chan error, 2)
		var wg sync.WaitGroup
		for i, d := range []*DB{a, b} {
			wg.Add(1)
			go func(i int, d *DB) {
				defer wg.Done()
				<-start
				_, err := d.SaveFocus([]string{[]string{"a", "b", "c", "d"}[int(base)*2+i]}, base)
				results <- err
			}(i, d)
		}
		close(start)
		wg.Wait()
		close(results)
		success, conflict := 0, 0
		for err := range results {
			if err == nil {
				success++
			} else if errors.Is(err, ErrFocusConflict) {
				conflict++
			} else {
				t.Fatal(err)
			}
		}
		if success != 1 || conflict != 1 {
			t.Fatal(success, conflict)
		}
	}
	f, err := a.GetFocus()
	if err != nil || f.Revision != 2 {
		t.Fatal(f, err)
	}
}

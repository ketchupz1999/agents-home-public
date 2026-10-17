package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreRejectsWritesThroughLinks(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	s := &Store{Dir: dir}
	if err := s.WriteFile("link", []byte("changed")); err == nil {
		t.Error("write followed symlink")
	}
	b, _ := os.ReadFile(outside)
	if string(b) != "unchanged" {
		t.Error("outside file changed")
	}
}

func TestCleanRejectsTraversalAndGitComponents(t *testing.T) {
	for _, p := range []string{"../secret", "/absolute", "profile/../secret", "profile/.git/config"} {
		if _, err := Clean(p); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
}

package store

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func snapshotGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test")
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatal(e, string(b))
	}
	return strings.TrimSpace(string(b))
}
func TestSnapshotsAndHistory(t *testing.T) {
	dir := t.TempDir()
	snapshotGit(t, dir, "init", "-b", "main")
	os.WriteFile(filepath.Join(dir, "file"), []byte("published"), 0600)
	snapshotGit(t, dir, "add", ".")
	snapshotGit(t, dir, "commit", "-m", "first")
	published := snapshotGit(t, dir, "rev-parse", "HEAD")
	snapshotGit(t, dir, "update-ref", "refs/remotes/origin/main", published)
	os.WriteFile(filepath.Join(dir, "file"), []byte("unpublished"), 0600)
	snapshotGit(t, dir, "commit", "-am", "second")
	s := &Store{Dir: dir, Remote: true}
	sha, err := s.PublishedSHA()
	if err != nil || sha != published {
		t.Fatal(sha, err)
	}
	b, err := s.ReadSnapshot(sha, "file")
	if err != nil || string(b) != "published" {
		t.Fatal(string(b), err)
	}
	b, hash, err := s.ReadWithSHA("file")
	if err != nil || string(b) != "unpublished" || hash != BlobSHA(b) {
		t.Fatal(hash, err)
	}
	os.WriteFile(filepath.Join(dir, "empty"), []byte{}, 0600)
	b, hash, err = s.ReadWithSHA("empty")
	if err != nil || b == nil || hash == "" {
		t.Fatal("empty confused with missing")
	}
	_, hash, err = s.ReadWithSHA("missing")
	if err == nil || hash != "" {
		t.Fatal("missing has hash")
	}
	page, err := s.History(HistoryQuery{Limit: 1})
	if err != nil || page.SHA != published || len(page.Commits) != 1 || page.Commits[0].Subject != "first" {
		t.Fatal(page, err)
	}
}

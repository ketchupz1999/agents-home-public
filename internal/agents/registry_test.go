package agents

import (
	"github.com/ketchupz1999/agents-home-public/internal/store"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPublishedRegistry(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatal(e, string(b))
		}
	}
	write := func(p, b string) {
		t.Helper()
		p = filepath.Join(dir, p)
		os.MkdirAll(filepath.Dir(p), 0700)
		if e := os.WriteFile(p, []byte(b), 0600); e != nil {
			t.Fatal(e)
		}
	}
	git("init", "-b", "main")
	write("agents/registry.yaml", "roles:\n  - id: team\n    character: codex\nagents:\n  - id: worker\n    role_id: team\n")
	write("agents/team/card.md", "---\ndisplay_name: Published\n---\n\ncard\n")
	git("add", ".")
	git("commit", "-m", "published")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	write("agents/registry.yaml", "roles: broken")
	write("agents/team/card.md", "---\ndisplay_name: Draft\n---\n\ncard\n")
	s := &store.Store{Dir: dir, Remote: true}
	r, e := Load(s)
	if e != nil || r.RoleFor("worker") != "team" || r.Cards["team"].DisplayName != "Published" {
		t.Fatal(r, e)
	}
	cases := []string{"roles: broken", "agents:\n - id: same\n - id: same", "roles:\n - id: team\n   character: https://bad", "roles:\n - id: team\nagents:\n - id: team", "agents:\n - id: worker\n   role_id: absent"}
	for _, raw := range cases {
		write("agents/registry.yaml", raw)
		git("add", ".")
		git("commit", "-m", "invalid")
		git("update-ref", "refs/remotes/origin/main", "HEAD")
		if _, e = Load(s); e == nil {
			t.Fatal("accepted", raw)
		}
	}
}

package store

import (
	"fmt"
	"strings"
	"time"
)

// ---- branch workflow ----
//
// Only the server pushes main. Agents push agent/<name>/<topic>; applying a
// branch rebases it onto main and fast-forwards, so main stays linear and no
// merge commit is ever created.

const BranchPrefix = "agent/"

// Branch is a pending or decided branch.
type Branch struct {
	Name    string   `json:"name"`
	Agent   string   `json:"agent"`
	Topic   string   `json:"topic"`
	Subject string   `json:"subject"`
	Date    string   `json:"date"`
	Files   []string `json:"files"`
}

// FetchBranches updates remote refs, so freshly pushed branches show up.
func (s *Store) FetchBranches() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.Remote {
		return nil
	}
	_, err := s.git("fetch", "--quiet", "--prune", "origin",
		"+refs/heads/"+BranchPrefix+"*:refs/remotes/origin/"+BranchPrefix+"*")
	return err
}

// ParseBranchName splits agent/<agent>/<topic...>. Names that do not match are
// not treated as agent branches.
func ParseBranchName(name string) (agent, topic string, ok bool) {
	rest, found := strings.CutPrefix(name, BranchPrefix)
	if !found {
		return "", "", false
	}
	agent, topic, found = strings.Cut(rest, "/")
	if !found || agent == "" || topic == "" {
		return "", "", false
	}
	return agent, topic, true
}

func (s *Store) ListBranches() ([]Branch, error) {
	out, err := s.git("for-each-ref", "--format=%(refname:short)%09%(authorname)%09%(authordate:short)%09%(subject)",
		"refs/remotes/origin/"+BranchPrefix)
	if err != nil {
		return nil, err
	}
	var branches []Branch
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		name := strings.TrimPrefix(f[0], "origin/")
		agent, topic, ok := ParseBranchName(name)
		if !ok {
			continue
		}
		b := Branch{Name: name, Agent: agent, Topic: topic}
		if len(f) > 1 {
			b.Subject = f[1]
		}
		if len(f) > 2 {
			b.Date = f[2]
		}
		if len(f) > 3 {
			b.Subject = f[3]
		}
		files, err := s.git("diff", "--name-only", "origin/main...origin/"+name)
		if err == nil {
			for _, fl := range strings.Split(strings.TrimSpace(files), "\n") {
				if fl = strings.TrimSpace(fl); fl != "" {
					b.Files = append(b.Files, fl)
				}
			}
		}
		branches = append(branches, b)
	}
	return branches, nil
}

func (s *Store) BranchDiff(name string) (string, error) {
	return s.ScopedBranchDiff(name, func(string) bool { return true })
}

// ScopedBranchDiff pins both refs under the store lock. Authorization and
// output use the exact same immutable commits, including both sides of renames.
func (s *Store) ScopedBranchDiff(name string, canRead func(string) bool) (string, error) {
	if _, _, ok := ParseBranchName(name); !ok {
		return "", ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.git("check-ref-format", "refs/heads/"+name); err != nil {
		return "", ErrNotFound
	}
	base, err := s.git("rev-parse", "--verify", "refs/remotes/origin/main^{commit}")
	if err != nil {
		return "", err
	}
	tip, err := s.git("rev-parse", "--verify", "refs/remotes/origin/"+name+"^{commit}")
	if err != nil {
		return "", ErrNotFound
	}
	rangeSpec := strings.TrimSpace(base) + "..." + strings.TrimSpace(tip)
	paths, err := s.git("diff", "--no-renames", "--name-only", "-z", rangeSpec, "--")
	if err != nil {
		return "", err
	}
	for _, p := range strings.Split(paths, "\x00") {
		if p == "" {
			continue
		}
		clean, err := Clean(p)
		if err != nil || clean != p || !canRead(p) {
			return "", ErrNotFound
		}
	}
	return s.git("diff", "--no-color", "--no-renames", rangeSpec, "--")
}

// ApplyBranch rebases name onto main and fast-forwards. It never merges with a
// merge commit; a conflict leaves the branch untouched and returns an error.
func (s *Store) ApplyBranch(name, actor string) error {
	agent, _, ok := ParseBranchName(name)
	if !ok {
		return fmt.Errorf("not an agent branch: %s", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.git("checkout", "--quiet", "main"); err != nil {
		return err
	}
	if s.Remote {
		if _, err := s.git("pull", "--rebase", "--quiet"); err != nil {
			_, _ = s.git("rebase", "--abort")
			return err
		}
	}
	// Detached rebase onto main, so a conflict cannot leave main half-merged.
	if _, err := s.git("checkout", "--quiet", "--detach", "origin/"+name); err != nil {
		return err
	}
	if _, err := s.git("rebase", "main"); err != nil {
		st, _ := s.git("status", "--short")
		_, _ = s.git("rebase", "--abort")
		_, _ = s.git("checkout", "--quiet", "main")
		return fmt.Errorf("rebase onto main failed for %s (%v); working tree was:\n%s", agent, err, st)
	}
	// After a detached rebase, fast-forward main to the rebased tip by sha:
	// git refuses `merge --ff-only` while HEAD is detached.
	tip, err2 := s.git("rev-parse", "HEAD")
	if err2 != nil {
		_, _ = s.git("checkout", "--quiet", "main")
		return err2
	}
	if _, err := s.git("checkout", "--quiet", "main"); err != nil {
		return err
	}
	if _, err := s.git("merge", "--ff-only", "--quiet", strings.TrimSpace(tip)); err != nil {
		return err
	}
	if s.Remote {
		if _, err := s.git("push", "--quiet", "origin", "main"); err != nil {
			return err
		}
		if _, err := s.git("push", "--quiet", "origin", "--delete", name); err != nil {
			return err
		}
	}
	_, err := s.git("checkout", "--quiet", "main")
	return err
}

// SyncMain pulls main and reports the current head, used by readers.
func (s *Store) SyncMain() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pullLocked()
}

// Log returns the last n commits of main, newest first.
func (s *Store) Log(n int) ([]string, error) {
	out, err := s.git("log", "--oneline", fmt.Sprintf("-%d", n))
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSpace(out), "\n"), nil
}

// CommitFile writes one file on main directly (used for agent cards) and pushes.
func (s *Store) CommitFile(rel string, data []byte, actor, msg string) error {
	return s.Write(actor, msg, func() ([]string, error) {
		return []string{rel}, s.WriteFile(rel, data)
	})
}

func (s *Store) Now() time.Time { return time.Now() }

// DeleteBranch removes a pending branch, used when the owner rejects it.
func (s *Store) DeleteBranch(name string) error {
	if _, _, ok := ParseBranchName(name); !ok {
		return fmt.Errorf("not an agent branch: %s", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.Remote {
		return nil
	}
	_, err := s.git("push", "--quiet", "origin", "--delete", name)
	return err
}

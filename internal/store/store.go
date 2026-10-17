// Package store owns the server's single clone of the data repo. Every write
// goes through Write, which serialises pull → change → commit → push.
package store

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	Dir      string        // working tree of the clone
	Remote   bool          // false in tests without an origin
	PullTTL  time.Duration // reads pull at most this often
	mu       sync.Mutex
	lastPull time.Time
}

// Clean validates a repo-relative path and returns it in slash form.
// Rejects absolute paths, "..", and anything under .git.
func Clean(p string) (string, error) {
	if path.IsAbs(p) || filepath.IsAbs(p) || strings.Contains(p, "\\") {
		return "", fmt.Errorf("invalid path")
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == ".git" {
			return "", fmt.Errorf("invalid path")
		}
	}
	p = strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(p)), "/")
	if p == "" || p == "." || p == ".git" || strings.HasPrefix(p, ".git/") {
		return "", fmt.Errorf("invalid path %q", p)
	}
	return p, nil
}

func (s *Store) abs(rel string) string { return filepath.Join(s.Dir, filepath.FromSlash(rel)) }

func (s *Store) git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = s.Dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

func (s *Store) pullLocked() error {
	if !s.Remote {
		return nil
	}
	if _, err := s.git("pull", "--rebase", "--quiet"); err != nil {
		_, _ = s.git("rebase", "--abort")
		return err
	}
	s.lastPull = time.Now()
	return nil
}

// Refresh pulls if the last pull is older than PullTTL, so pushes made
// directly with git become visible to API readers.
func (s *Store) Refresh() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.lastPull) < s.PullTTL {
		return nil
	}
	return s.pullLocked()
}

// Write runs fn against the freshest tree and commits whatever it changed.
// fn returns the repo-relative paths it touched.
func (s *Store) Write(author, msg string, fn func() ([]string, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.pullLocked(); err != nil {
		return err
	}
	paths, err := fn()
	if err != nil {
		_, _ = s.git("checkout", "--", ".")
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	if _, err := s.git(append([]string{"add", "--"}, paths...)...); err != nil {
		return err
	}
	if _, err := s.git("-c", "user.name="+author, "-c", "user.email="+author+"@agents-shared-server",
		"commit", "--quiet", "-m", msg); err != nil {
		return err
	}
	if !s.Remote {
		return nil
	}
	var pushErr error
	for i := 0; i < 3; i++ {
		if _, pushErr = s.git("push", "--quiet"); pushErr == nil {
			return nil
		}
		if err := s.pullLocked(); err != nil {
			return err
		}
	}
	return pushErr
}

func (s *Store) Read(rel string) ([]byte, error) {
	return s.readScoped(rel, nil)
}

// WriteFile is only valid inside a Write callback.
func (s *Store) WriteFile(rel string, data []byte) error {
	return s.writeBounded(rel, data, false)
}

// CreateFile fails if rel already exists, so entries stay append-only.
func (s *Store) CreateFile(rel string, data []byte) error {
	return s.writeBounded(rel, data, true)
}

func (s *Store) Exists(rel string) bool {
	f, err := s.openRead(rel, nil)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// List returns repo-relative file paths under dir (recursive), skipping dotfiles.
func (s *Store) List(dir string) ([]string, error) {
	var out []string
	anchor, tree, err := s.repoFS(dir)
	if err != nil {
		return nil, err
	}
	defer anchor.Close()
	if dir != "." && !s.Exists(dir) {
		return nil, nil
	}
	root := dir
	err = fs.WalkDir(tree, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		if strings.HasPrefix(d.Name(), ".") && p != root {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			if s.Exists(p) {
				out = append(out, p)
			}
		}
		return nil
	})
	return out, err
}

// Dirs returns immediate subdirectory names of dir.
func (s *Store) Dirs(dir string) ([]string, error) {
	f, err := s.openRead(dir, nil)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	ents, err := f.ReadDir(-1)
	var out []string
	for _, e := range ents {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out, err
}

// BlobSHA matches `git hash-object`, so clients can compute it offline too.
// Missing files hash to "".
func BlobSHA(data []byte) string {
	if data == nil {
		return ""
	}
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

package store

import (
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// openRead confines traversal to the repository and, for API callers, to a
// granted directory. Root enforces the boundary during open, including symlink
// swaps. A cross-grant alias is intentionally refused: use the target path.
func (s *Store) openRead(rel string, scopes []string) (*os.File, error) {
	clean, err := Clean(rel)
	if err != nil || clean != rel {
		return nil, ErrNotFound
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	// Resolve once to exclude aliases into Git's internal files as well.
	actual, err := filepath.EvalSymlinks(s.abs(rel))
	if err != nil {
		return nil, ErrNotFound
	}
	base, err := filepath.EvalSymlinks(s.Dir)
	if err != nil {
		return nil, err
	}
	target, err := filepath.Rel(base, actual)
	target = filepath.ToSlash(target)
	if err != nil || target == ".." || strings.HasPrefix(target, "../") {
		return nil, ErrNotFound
	}
	if c, e := Clean(target); e != nil || c != target {
		return nil, ErrNotFound
	}
	if scopes == nil {
		scopes = []string{"*"}
	}
	for _, scope := range scopes {
		scope = strings.TrimSuffix(scope, "/")
		if scope == "*" {
			return root.Open(target)
		}
		if c, e := Clean(scope); e != nil || c != scope {
			continue
		}
		if rel != scope && !strings.HasPrefix(rel, scope+"/") {
			continue
		}
		if target != scope && !strings.HasPrefix(target, scope+"/") {
			continue
		}
		sub, e := root.OpenRoot(scope)
		if e == nil {
			suffix := strings.TrimPrefix(target, scope+"/")
			f, openErr := sub.Open(suffix)
			sub.Close()
			if openErr == nil {
				return f, nil
			}
			continue
		}
		// An exact-file grant cannot be used as an alias to its siblings.
		if target == scope && rel == scope {
			parent, e := root.OpenRoot(path.Dir(scope))
			if e != nil {
				continue
			}
			f, e := parent.Open(path.Base(scope))
			parent.Close()
			if e == nil {
				return f, nil
			}
		}
	}
	return nil, ErrNotFound
}

func (s *Store) readScoped(rel string, scopes []string) ([]byte, error) {
	f, err := s.openRead(rel, scopes)
	if err != nil {
		return nil, ErrNotFound
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrNotFound
	}
	return io.ReadAll(f)
}

// ReadScopedWithSHA checks grants against both the name and the bounded target.
func (s *Store) ReadScopedWithSHA(rel string, scopes []string) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.readScoped(rel, scopes)
	if err != nil {
		return nil, "", err
	}
	return b, BlobSHA(b), nil
}

func (s *Store) writeBounded(rel string, data []byte, exclusive bool) error {
	clean, err := Clean(rel)
	if err != nil || clean != rel {
		return fs.ErrPermission
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return err
	}
	defer root.Close()
	// Agent-owned writes must never follow an alias into someone else's data.
	parts := strings.Split(rel, "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fs.ErrPermission
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := root.MkdirAll(path.Dir(rel), 0755); err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if exclusive {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	f, err := root.OpenFile(rel, flags, 0644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// repoFS anchors directory enumeration at the same boundary as file reads.
func (s *Store) repoFS(dir string) (*os.Root, fs.FS, error) {
	if dir != "." {
		if c, e := Clean(dir); e != nil || c != dir {
			return nil, nil, ErrNotFound
		}
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return nil, nil, err
	}
	return root, root.FS(), nil
}

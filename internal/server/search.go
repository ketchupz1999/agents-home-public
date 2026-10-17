package server

import (
	"bufio"
	"bytes"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ketchupz1999/agents-home-public/internal/store"
)

// Full-text search without an index: the repo is a few hundred text files, so
// scanning on each request is milliseconds and can never drift from git.
// Add an FTS index only when this becomes slow.

const (
	searchMaxFileSize = 2 << 20 // skip anything this large: it is not prose
	searchDefaultMax  = 50
	snippetRunes      = 60
)

var searchExts = map[string]bool{
	".md": true, ".txt": true, ".yaml": true, ".yml": true, ".json": true, ".html": true, ".csv": true,
}

type hit struct {
	Kind    string `json:"kind"` // file | message
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
	ID      string `json:"id,omitempty"`
	Item    string `json:"item,omitempty"`
	Title   string `json:"title,omitempty"`
	Snippet string `json:"snippet"`
}

func (s *Server) search(w http.ResponseWriter, r *http.Request, k *Key) {
	q := r.URL.Query()
	needle := strings.TrimSpace(q.Get("q"))
	if utf8.RuneCountInString(needle) < 2 {
		fail(w, http.StatusBadRequest, "q must be at least 2 characters")
		return
	}
	max := searchDefaultMax
	if n, err := strconv.Atoi(q.Get("max")); err == nil && n > 0 && n <= 500 {
		max = n
	}
	scope := ""
	if in := strings.Trim(q.Get("in"), "/"); in != "" && in != "inbox" {
		clean, err := store.Clean(in) // rejects "..", absolute paths and .git
		if err != nil || !k.CanRead(clean) {
			fail(w, http.StatusNotFound, "scope not found")
			return
		}
		scope = clean
	} else if in == "inbox" {
		scope = "inbox"
	}
	lower := strings.ToLower(needle)

	hits := []hit{}
	truncated := false
	add := func(h hit) bool {
		if len(hits) >= max {
			truncated = true
			return false
		}
		hits = append(hits, h)
		return true
	}

	root := s.Store.Dir
	if scope != "" && scope != "inbox" {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(scope))); err != nil {
			fail(w, http.StatusNotFound, "scope not found")
			return
		}
	}
	walkRoot := filepath.Join(root, filepath.FromSlash(scope))
	if scope == "inbox" {
		walkRoot = "" // messages only
	}
	_ = walkIf(walkRoot != "", walkRoot, func(p string, d os.DirEntry, err error) error {
		if truncated {
			return filepath.SkipAll
		}
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") && p != walkRoot {
				return filepath.SkipDir
			}
			return nil
		}
		if !searchExts[strings.ToLower(filepath.Ext(name))] {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if !k.CanRead(rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > searchMaxFileSize {
			return nil
		}
		data, _, err := s.Store.ReadScopedWithSHA(rel, k.readScopes())
		if err != nil || !bytes.Contains(bytes.ToLower(data), []byte(lower)) {
			return nil
		}
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for n := 1; sc.Scan(); n++ {
			line := sc.Text()
			if strings.Contains(strings.ToLower(line), lower) {
				if !add(hit{Kind: "file", Path: rel, Line: n, Snippet: snippet(line, lower)}) {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})

	// Mailbox messages live in the database, not in git.
	if scope == "" || scope == "group" {
		msgs, err := s.DB.ListGroupMessagesAll(0)
		if err == nil {
			for _, m := range msgs {
				if !k.CanRead("items/"+m.Item) || !s.canSeeGroup(m.GroupID, k) {
					continue
				}
				text := m.Task + "\n" + m.Body
				if i := strings.Index(strings.ToLower(text), lower); i >= 0 {
					line := text[strings.LastIndex(text[:i], "\n")+1:]
					if j := strings.IndexByte(line, '\n'); j >= 0 {
						line = line[:j]
					}
					if !add(hit{Kind: "message", ID: m.ID, Item: m.Item, Title: m.Task, Snippet: snippet(line, lower)}) {
						break
					}
				}
			}
		}
	}
	w.Header().Set("X-ASpace-Search-Hits", strconv.Itoa(len(hits)))
	ok(w, map[string]any{"q": needle, "hits": hits, "truncated": truncated})
}

func walkIf(on bool, root string, fn fs.WalkDirFunc) error {
	if !on {
		return nil
	}
	return filepath.WalkDir(root, fn)
}

// snippet trims a long line to a window around the first match.
func snippet(line, lower string) string {
	line = strings.TrimSpace(line)
	runes := []rune(line)
	if len(runes) <= snippetRunes*2 {
		return line
	}
	idx := strings.Index(strings.ToLower(line), lower)
	start := utf8.RuneCountInString(line[:max(idx, 0)]) - snippetRunes
	if start < 0 {
		start = 0
	}
	end := start + snippetRunes*2
	if end > len(runes) {
		end = len(runes)
	}
	out := string(runes[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}

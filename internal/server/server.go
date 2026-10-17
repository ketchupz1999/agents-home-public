// Package server exposes the data repo over HTTP. Slow-moving content lives in
// git and is edited with ordinary git; in-flight things (mailbox messages, who
// read what, branch decisions) live in the SQLite store instead.
package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/ketchupz1999/agents-home-public/internal/agents"
	"github.com/ketchupz1999/agents-home-public/internal/db"
	"github.com/ketchupz1999/agents-home-public/internal/protocol"
	"github.com/ketchupz1999/agents-home-public/internal/store"
	"github.com/ketchupz1999/agents-home-public/internal/web"
)

type Server struct {
	Store *store.Store
	Keys  *KeyFile
	DB    *db.DB
	Web   *web.Web // optional mobile dashboard
	Now   func() time.Time
}

// WebHandler returns nil when the dashboard is disabled.
func (s *Server) webHandler() http.Handler {
	if s.Web == nil {
		return nil
	}
	return s.Web.Handler()
}

const maxBody = 1 << 20 // messages are small Markdown blobs

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	route := func(pattern string, h func(http.ResponseWriter, *http.Request, *Key)) {
		mux.HandleFunc(pattern, s.auth(h))
	}
	route("GET /v1/actions", s.listActions)
	route("GET /v1/actions/{id}", s.getAction)
	route("POST /v1/actions", s.postAction)
	route("GET /v1/whoami", s.whoami)
	route("POST /v1/runs", s.registerRun)
	route("GET /v1/runs/{id}", s.getRun)
	route("GET /v1/now", s.getNow)
	route("GET /v1/brief/{id}", s.getBrief)
	route("GET /v1/items", s.listItems)
	route("GET /v1/items/{id}", s.getItem)
	route("GET /v1/file", s.getFile)
	route("PUT /v1/cards/me", s.putCard)
	route("GET /v1/branches", s.listBranches)
	// Branch names contain slashes (agent/<name>/<topic>), so the wildcard
	// captures the rest of the path and the suffix is stripped in the handler.
	route("GET /v1/branches/{name...}", s.branchDiff)
	route("POST /v1/branches/{name...}", s.decideBranch)
	route("GET /v1/log", s.getLog)
	route("GET /v1/search", s.search)
	// ---- groups ----
	route("POST /v1/groups", s.createGroup)
	route("GET /v1/groups", s.listGroups)
	route("GET /v1/groups/{id}", s.getGroup)
	route("GET /v1/groups/{id}/messages", s.listGroupMessages)
	route("POST /v1/groups/{id}/messages", s.postGroupMessage)
	route("POST /v1/groups/{id}/messages/{message}/ack", s.ackGroupMessage)
	route("POST /v1/groups/{id}/close", s.closeGroup)
	route("POST /v1/groups/{id}/members", s.changeGroupMembers)
	// ---- wakeups ----
	route("GET /v1/wakeups", s.pollWakeups)
	route("POST /v1/wakeups/{id}/claim", s.claimWakeup)
	route("POST /v1/wakeups/{id}/finish", s.finishWakeup)
	if s.Web != nil {
		mux.Handle("/d/", s.Web.Handler())
	}
	return logRequests(mux)
}

func (s *Server) auth(h func(http.ResponseWriter, *http.Request, *Key)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		k := s.Keys.Lookup(strings.TrimSpace(secret))
		if !ok || k == nil {
			fail(w, http.StatusUnauthorized, "invalid key")
			return
		}
		w.Header().Set("X-Agent", k.Name)
		var valid bool
		r, valid = s.validateRun(w, r, k)
		if !valid {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		if r.Method == http.MethodGet {
			if err := s.Store.Refresh(); err != nil {
				log.Printf("refresh: %v", err)
			}
		}
		h(w, r, k)
	}
}

// ---- identity and repo reads ----

func (s *Server) whoami(w http.ResponseWriter, r *http.Request, k *Key) {
	// Hard permissions live here, not in a file: agents ask instead of guessing.
	out := map[string]any{"name": k.Name, "owner": k.Owner, "approve": k.CanDecide(), "read": k.Read}
	if registry, err := agents.Load(s.Store); err == nil {
		out["role_id"] = registry.RoleFor(k.Name)
		out["registry_sha"] = registry.SHA
	} else {
		out["role_status"] = "unavailable"
	}
	ok(w, out)
}

func (s *Server) listItems(w http.ResponseWriter, r *http.Request, k *Key) {
	ids, err := s.Store.Dirs("items")
	if err != nil {
		internal(w, err)
		return
	}
	msgs, err := s.DB.ListUnreadGroupMessages(k.Name)
	if err != nil {
		internal(w, err)
		return
	}
	unread := map[string]int{}
	for _, m := range msgs {
		if !s.canSeeGroup(m.GroupID, k) {
			continue
		}
		unread[m.Item]++
	}
	type row struct {
		ID     string `json:"id"`
		Unread int    `json:"unread"`
	}
	out := []row{}
	for _, id := range ids {
		if k.CanRead("items/" + id) {
			out = append(out, row{id, unread[id]})
		}
	}
	ok(w, out)
}

func (s *Server) getItem(w http.ResponseWriter, r *http.Request, k *Key) {
	id := r.PathValue("id")
	dir := "items/" + id
	if !protocol.ValidName(id) || !k.CanRead(dir) || !s.Store.Exists(dir) {
		fail(w, http.StatusNotFound, "item not found")
		return
	}
	files, err := s.Store.List(dir)
	if err != nil {
		internal(w, err)
		return
	}
	ctx, sha, readErr := s.Store.ReadScopedWithSHA(dir+"/CONTEXT.md", k.readScopes())
	if readErr == nil {
		s.observe(w, r, k, "file.served", dir+"/CONTEXT.md", sha, map[string]string{"endpoint": "item"})
	}
	ok(w, map[string]any{"id": id, "context": string(ctx), "files": files})
}

func (s *Server) getFile(w http.ResponseWriter, r *http.Request, k *Key) {
	rel, err := store.Clean(r.URL.Query().Get("path"))
	if err != nil || !k.CanRead(rel) {
		fail(w, http.StatusNotFound, "file not found")
		return
	}
	b, sha, err := s.Store.ReadScopedWithSHA(rel, k.readScopes())
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "file not found")
		return
	} else if err != nil {
		internal(w, err)
		return
	}
	s.observe(w, r, k, "file.served", rel, sha, map[string]string{"endpoint": "file"})
	ok(w, map[string]any{"path": rel, "content": string(b)})
}

func (s *Server) getLog(w http.ResponseWriter, r *http.Request, k *Key) {
	// Commit subjects are repository-wide metadata, not scoped file content.
	fullRead := k.Owner
	for _, scope := range k.Read {
		if scope == "*" {
			fullRead = true
		}
	}
	if !fullRead {
		fail(w, http.StatusForbidden, "repository-wide read permission required")
		return
	}
	lines, err := s.Store.Log(20)
	if err != nil {
		internal(w, err)
		return
	}
	ok(w, lines)
}

// ---- inbox (database) ----

func (s *Server) putCard(w http.ResponseWriter, r *http.Request, k *Key) {
	var in struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Content) == "" {
		fail(w, http.StatusBadRequest, "content required")
		return
	}
	if err := s.saveCard(k.Name, []byte(in.Content)); err != nil {
		internal(w, err)
		return
	}
	ok(w, map[string]any{"path": "agents/" + k.Name + "/card.md"})
}

// ---- helpers ----

func ok(w http.ResponseWriter, v any) { writeJSON(w, http.StatusOK, v) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func internal(w http.ResponseWriter, err error) {
	log.Printf("error: %v", err)
	fail(w, http.StatusInternalServerError, "internal error")
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (sw *statusWriter) WriteHeader(c int) { sw.code = c; sw.ResponseWriter.WriteHeader(c) }

// logRequests records request metadata and a verified proxy IP, never query
// strings, credentials, bodies, or the raw forwarding header.
func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		h.ServeHTTP(sw, r)
		log.Printf("%s %q %d %s agent=%q ip=%s search_hits=%s", r.Method, r.URL.Path, sw.code,
			time.Since(start).Round(time.Millisecond), w.Header().Get("X-Agent"), requestIP(r), w.Header().Get("X-ASpace-Search-Hits"))
	})
}

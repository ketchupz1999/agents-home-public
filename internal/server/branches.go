package server

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/ketchupz1999/agents-home-public/internal/db"
	"github.com/ketchupz1999/agents-home-public/internal/store"
)

// Branches are how a remote agent proposes a change: it pushes
// agent/<name>/<topic> and the owner merges it. Applying always rebases onto
// main and fast-forwards, so main stays linear.

func (s *Server) listBranches(w http.ResponseWriter, r *http.Request, k *Key) {
	if err := s.Store.FetchBranches(); err != nil {
		internal(w, err)
		return
	}
	branches, err := s.Store.ListBranches()
	if err != nil {
		internal(w, err)
		return
	}
	status := r.URL.Query().Get("status")
	out := []store.Branch{}
	for _, b := range branches {
		visible := len(b.Files) > 0 || k.Owner
		for _, p := range b.Files {
			if !k.CanRead(p) {
				visible = false
				break
			}
		}
		if !visible {
			continue
		}
		if _, err := s.Store.ScopedBranchDiff(b.Name, k.CanRead); err != nil {
			continue
		}
		if dec := s.DB.BranchStatus(b.Name); dec == "rejected" {
			continue // decided branches disappear from the pending list
		}
		if status == "pending" || status == "" {
			out = append(out, b)
		}
	}
	ok(w, out)
}

func (s *Server) branchDiff(w http.ResponseWriter, r *http.Request, k *Key) {
	name := strings.TrimSuffix(r.PathValue("name"), "/diff")
	if err := s.Store.FetchBranches(); err != nil {
		internal(w, err)
		return
	}
	diff, err := s.Store.ScopedBranchDiff(name, k.CanRead)
	if err != nil {
		fail(w, http.StatusNotFound, "branch not found")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(diff))
}

// decideBranch reads the action from the URL suffix: .../apply or .../reject.
func (s *Server) decideBranch(w http.ResponseWriter, r *http.Request, k *Key) {
	name := r.PathValue("name")
	action := ""
	switch {
	case strings.HasSuffix(name, "/apply"):
		name, action = strings.TrimSuffix(name, "/apply"), "apply"
	case strings.HasSuffix(name, "/reject"):
		name, action = strings.TrimSuffix(name, "/reject"), "reject"
	default:
		fail(w, http.StatusNotFound, "use POST /v1/branches/<branch>/apply or /reject")
		return
	}
	agent, topic, valid := store.ParseBranchName(name)
	if !valid {
		fail(w, http.StatusNotFound, "not an agent branch: use agent/<name>/<topic>")
		return
	}
	if !k.CanDecide() && !k.Owner {
		fail(w, http.StatusForbidden, "this key cannot apply or reject branches")
		return
	}
	rec := db.BranchDecision{Name: name, Agent: agent, Topic: topic,
		CreatedAt: time.Now().UTC().Format(time.RFC3339), DecidedBy: k.Name}
	switch action {
	case "apply":
		if err := s.Store.ApplyBranch(name, k.Name); err != nil {
			rec.Status = "conflict"
			_ = s.DB.RecordBranch(rec)
			s.DB.Audit(k.Name, "branch.conflict", name)
			writeJSON(w, http.StatusConflict, map[string]any{"name": name, "status": "conflict",
				"error": "conflict with main; " + agent + " should rebase and push again", "detail": err.Error()})
			return
		}
		rec.Status = "applied"
	case "reject":
		if err := s.Store.DeleteBranch(name); err != nil {
			internal(w, err)
			return
		}
		rec.Status = "rejected"
	}
	// A merge can change goals/lines/items, so the generated overview follows it.
	if err := s.refreshNow(k.Name); err != nil {
		log.Printf("refresh NOW.md: %v", err)
	}
	rec.DecidedAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.DB.RecordBranch(rec); err != nil {
		internal(w, err)
		return
	}
	s.DB.Audit(k.Name, "branch."+rec.Status, name)
	ok(w, map[string]any{"name": name, "status": rec.Status})
}

// pendingSummary is used by the dashboard later; kept here so the query lives
// next to the branch logic.
func (s *Server) pendingSummary() (int, error) {
	branches, err := s.Store.ListBranches()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, b := range branches {
		if s.DB.BranchStatus(b.Name) != "rejected" {
			n++
		}
	}
	return n, nil
}

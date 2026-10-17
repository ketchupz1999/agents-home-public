package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ketchupz1999/agents-home-public/internal/db"
	"io"
	"log"
	"net/http"
	"time"
)

type runContextKey struct{}

func requestRun(r *http.Request) *db.Run {
	v, _ := r.Context().Value(runContextKey{}).(*db.Run)
	return v
}
func (s *Server) validateRun(w http.ResponseWriter, r *http.Request, k *Key) (*http.Request, bool) {
	id := r.Header.Get("X-ASpace-Run-ID")
	if id == "" {
		return r, true
	}
	if !db.ValidRunID(id) {
		fail(w, 400, "invalid run ID")
		return r, false
	}
	run, err := s.DB.GetRun(k.Name, id)
	if errors.Is(err, db.ErrRunNotFound) {
		fail(w, 404, "run not found")
		return r, false
	}
	if err != nil {
		internal(w, err)
		return r, false
	}
	return r.WithContext(context.WithValue(r.Context(), runContextKey{}, run)), true
}
func (s *Server) registerRun(w http.ResponseWriter, r *http.Request, k *Key) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var in db.Run
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&in); err != nil {
		fail(w, 400, "invalid run JSON")
		return
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		fail(w, 400, "invalid run JSON")
		return
	}
	in.PrincipalID = k.Name
	if err := db.ValidateRun(in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	run, err := s.DB.RegisterRun(in)
	if errors.Is(err, db.ErrRunConflict) {
		fail(w, 409, "run attributes conflict")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	ok(w, run)
}
func (s *Server) getRun(w http.ResponseWriter, r *http.Request, k *Key) {
	id := r.PathValue("id")
	if !db.ValidRunID(id) {
		fail(w, 400, "invalid run ID")
		return
	}
	run, err := s.DB.GetRun(k.Name, id)
	if errors.Is(err, db.ErrRunNotFound) {
		fail(w, 404, "run not found")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	ok(w, run)
}
func (s *Server) observe(w http.ResponseWriter, r *http.Request, k *Key, kind, resource, sha string, metadata any) {
	raw, err := json.Marshal(metadata)
	if err == nil {
		o := db.Observation{PrincipalID: k.Name, Kind: kind, Resource: resource, ContentSHA: sha, MetadataJSON: string(raw), OccurredAt: time.Now().UTC().Format(db.TimestampFormat)}
		if run := requestRun(r); run != nil {
			o.RunID = run.ClientRunID
		}
		err = s.DB.RecordObservation(o)
	}
	if err != nil {
		w.Header().Set("X-ASpace-Observation", "unavailable")
		log.Printf("observation unavailable kind=%s", kind)
	}
}

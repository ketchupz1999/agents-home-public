package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/ketchupz1999/agents-home-public/internal/protocol"
)

// pollWakeups returns pending wakeup tasks for the caller.
// Supports long-polling via ?wait=N (seconds, max 60).
func (s *Server) pollWakeups(w http.ResponseWriter, r *http.Request, k *Key) {
	limit := 50
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 200 {
		limit = n
	}
	waitSec := 0
	if n, err := strconv.Atoi(r.URL.Query().Get("wait")); err == nil && n > 0 {
		if n > 60 {
			n = 60
		}
		waitSec = n
	}

	// Immediate check.
	tasks, err := s.DB.PollWakeups(k.Name, limit)
	if err != nil {
		internal(w, err)
		return
	}
	if len(tasks) > 0 || waitSec == 0 {
		ok(w, tasks)
		return
	}

	// Long-poll: check every 2s until data or timeout.
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(waitSec)*time.Second)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			ok(w, []any{})
			return
		case <-ticker.C:
			tasks, err = s.DB.PollWakeups(k.Name, limit)
			if err != nil {
				internal(w, err)
				return
			}
			if len(tasks) > 0 {
				ok(w, tasks)
				return
			}
		}
	}
}

// claimWakeup atomically claims a pending task.
func (s *Server) claimWakeup(w http.ResponseWriter, r *http.Request, k *Key) {
	id := r.PathValue("id")
	var in struct {
		Consumer string `json:"consumer"`
	}
	// An empty body is a valid claim (all fields optional) — treat EOF as {}.
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil && err != io.EOF {
		fail(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	if in.Consumer == "" {
		in.Consumer = k.Name
	}

	wk, err := s.DB.ClaimWakeup(id, k.Name, in.Consumer)
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	s.DB.Audit(k.Name, "wakeup.claim", id)
	ok(w, wk)
}

// finishWakeup completes a claimed task, optionally posting a reply.
func (s *Server) finishWakeup(w http.ResponseWriter, r *http.Request, k *Key) {
	id := r.PathValue("id")
	var in struct {
		ClaimToken   string `json:"claim_token"`
		Result       string `json:"result"` // replied | failed
		Body         string `json:"body"`
		ErrorSummary string `json:"error_summary"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	if in.ClaimToken == "" {
		fail(w, http.StatusBadRequest, "claim_token required")
		return
	}
	if in.Result != "replied" && in.Result != "failed" {
		fail(w, http.StatusBadRequest, "result must be 'replied' or 'failed'")
		return
	}

	// Build the reply without side effects; the DB transaction validates and writes it.
	var replyMsgID string
	var reply *protocol.Message
	if in.Result == "replied" {
		wk, err := s.DB.GetWakeup(id)
		if err != nil || wk == nil {
			fail(w, http.StatusNotFound, "wakeup not found")
			return
		}
		if wk.GroupID == "" {
			fail(w, http.StatusConflict, "wakeup has no group")
			return
		}
		orig, err := s.DB.GetMessage(wk.MessageID)
		if err != nil || orig == nil {
			fail(w, http.StatusNotFound, "original message not found")
			return
		}

		now := s.Now()
		reply = &protocol.Message{
			ID:          protocol.NewID(k.Name, now),
			From:        k.Name,
			FromRole:    s.roleFor(k.Name),
			To:          "group:" + orig.GroupID,
			Item:        orig.Item,
			Kind:        protocol.KindReply,
			GroupID:     orig.GroupID,
			ReplyTo:     orig.ID,
			RequestedBy: requestedBy(k),
			CreatedAt:   now.UTC(),
			Body:        in.Body,
			Task:        "Re: " + orig.Task,
		}
		replyMsgID = reply.ID
	}

	if err := s.DB.FinishWakeup(id, k.Name, in.ClaimToken, in.Result, in.ErrorSummary, k.Owner, reply); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	s.DB.Audit(k.Name, "wakeup.finish", id+" result="+in.Result)
	ok(w, map[string]any{"id": id, "status": in.Result, "reply_message_id": replyMsgID})
}

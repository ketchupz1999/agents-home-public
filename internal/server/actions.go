package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/ketchupz1999/agents-home-public/internal/agents"
	"github.com/ketchupz1999/agents-home-public/internal/db"
	"github.com/ketchupz1999/agents-home-public/internal/protocol"
	"github.com/ketchupz1999/agents-home-public/internal/store"
)

// canReadAction 同时检查事项和原始来源，角色与运行声明不扩大读取范围。
func (s *Server) canReadAction(k *Key, a db.Action) (bool, error) {
	if !k.CanRead("items/" + a.Item) {
		return false, nil
	}
	if a.SourcePath != "" {
		return k.CanRead(a.SourcePath), nil
	}
	m, err := s.DB.GetMessage(a.SourceMessageID)
	if err != nil {
		return false, err
	}
	return m != nil && m.Item == a.Item && k.CanRead("items/"+m.Item) && s.canSeeGroup(m.GroupID, k), nil
}

func (s *Server) listActions(w http.ResponseWriter, r *http.Request, k *Key) {
	q := r.URL.Query()
	item, status := q.Get("item"), q.Get("status")
	if (item != "" && !protocol.ValidName(item)) || (status != "" && status != "suggested" && status != "planned" && status != "done" && status != "dismissed") {
		fail(w, 400, "invalid action filter")
		return
	}
	actions, err := s.DB.ListActions(item, status)
	if err != nil {
		internal(w, err)
		return
	}
	out := []db.Action{}
	for _, a := range actions {
		visible, err := s.canReadAction(k, a)
		if err != nil {
			internal(w, err)
			return
		}
		if visible {
			out = append(out, a)
		}
	}
	ok(w, out)
}

func (s *Server) getAction(w http.ResponseWriter, r *http.Request, k *Key) {
	a, err := s.DB.GetAction(r.PathValue("id"))
	if err != nil {
		internal(w, err)
		return
	}
	if a == nil {
		fail(w, 404, "action not found")
		return
	}
	visible, err := s.canReadAction(k, *a)
	if err != nil {
		internal(w, err)
		return
	}
	if !visible {
		fail(w, 404, "action not found")
		return
	}
	history, err := s.DB.ActionHistory(a.ID)
	if err != nil {
		internal(w, err)
		return
	}
	ok(w, map[string]any{"action": a, "history": history})
}

// postAction 只接收建议字段，客户端无法提交身份、权限或已采纳状态。
func (s *Server) postAction(w http.ResponseWriter, r *http.Request, k *Key) {
	var p db.ActionProposal
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		fail(w, 400, "invalid action json")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		fail(w, 400, "invalid action json")
		return
	}
	if err := db.ValidateActionProposal(p); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !k.CanRead("items/" + p.Item) {
		fail(w, 404, "item not found")
		return
	}
	if p.SourcePath != "" {
		rel, err := store.Clean(p.SourcePath)
		if err != nil || !k.CanRead(rel) {
			fail(w, 404, "source not found")
			return
		}
		p.SourcePath = rel
	}
	// 响应丢失后的重试先找原记录；来源移除不能把已创建的行动变成未创建。
	prior, err := s.DB.RetryAction(p, k.Name)
	if errors.Is(err, db.ErrActionConflict) {
		fail(w, 409, "client_id already used for another proposal")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	if prior != nil {
		visible, err := s.canReadAction(k, *prior)
		if err != nil {
			internal(w, err)
			return
		}
		if !visible {
			fail(w, 404, "action not found")
			return
		}
		ok(w, prior)
		return
	}
	if !s.Store.Exists("items/" + p.Item) {
		_ = s.Store.Refresh()
		if !s.Store.Exists("items/" + p.Item) {
			fail(w, 404, "item not found")
			return
		}
	}
	sourceSHA := ""
	if p.SourceMessageID != "" {
		m, err := s.DB.GetMessage(p.SourceMessageID)
		if err != nil {
			internal(w, err)
			return
		}
		if m == nil || m.Item != p.Item || !k.CanRead("items/"+m.Item) || !s.canSeeGroup(m.GroupID, k) {
			fail(w, 404, "source not found")
			return
		}
	} else {
		rel, err := store.Clean(p.SourcePath)
		if err != nil || !k.CanRead(rel) {
			fail(w, 404, "source not found")
			return
		}
		p.SourcePath = rel
		_, sourceSHA, err = s.Store.ReadScopedWithSHA(rel, k.readScopes())
		if errors.Is(err, store.ErrNotFound) {
			fail(w, 404, "source not found")
			return
		}
		if err != nil {
			internal(w, err)
			return
		}
	}
	registry, err := agents.Load(s.Store)
	if err != nil {
		fail(w, 503, "published agent registry unavailable")
		return
	}
	actor := db.ActionActor{Name: k.Name, Role: registry.RoleFor(k.Name), Owner: k.Owner}
	if run := requestRun(r); run != nil {
		actor.Runtime = &protocol.Runtime{RunID: run.ClientRunID, Harness: run.Harness, Workspace: run.Workspace, DeviceID: run.DeviceID, Source: "client_reported"}
	}
	a, created, err := s.DB.CreateAction(p, actor, sourceSHA)
	if errors.Is(err, db.ErrActionConflict) {
		fail(w, 409, "client_id already used for another proposal")
		return
	}
	if errors.Is(err, db.ErrActionValidation) {
		fail(w, 400, "invalid action")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	writeJSON(w, code, a)
}

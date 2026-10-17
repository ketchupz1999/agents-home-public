package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/ketchupz1999/agents-home-public/internal/agents"
	"github.com/ketchupz1999/agents-home-public/internal/db"
	"github.com/ketchupz1999/agents-home-public/internal/protocol"
)

// GroupInput is what a creator supplies; the API and the dashboard share it.
type GroupInput = protocol.GroupInput

// GroupError carries the HTTP status a failed creation maps to.
type GroupError struct {
	Status int
	Msg    string
}

func (e *GroupError) Error() string { return e.Msg }

// HTTPStatus lets the dashboard map the failure without importing this package.
func (e *GroupError) HTTPStatus() int { return e.Status }

// createGroup builds a discussion group and posts its opening message atomically.
func (s *Server) createGroup(w http.ResponseWriter, r *http.Request, k *Key) {
	var in GroupInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	created, rootID, err := s.CreateGroup(k, in)
	if ge, isGE := err.(*GroupError); isGE {
		fail(w, ge.Status, ge.Msg)
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"group":           created,
		"root_message_id": rootID,
	})
}

// CreateGroupAs runs CreateGroup for a named key; the dashboard uses it so
// web-created groups follow exactly the same rules as `aspace group create`.
func (s *Server) CreateGroupAs(name string, in GroupInput) (*protocol.Group, string, error) {
	for _, k := range s.Keys.List() {
		if k.Name == name {
			return s.CreateGroup(&k, in)
		}
	}
	return nil, "", &GroupError{http.StatusForbidden, "unknown creator"}
}

// CreateGroup validates the input, stores the group and its opening message,
// and wakes the mentioned members.
func (s *Server) CreateGroup(k *Key, in GroupInput) (*protocol.Group, string, error) {
	if strings.TrimSpace(in.Topic) == "" || strings.TrimSpace(in.Item) == "" || len(in.Members) == 0 {
		return nil, "", &GroupError{http.StatusBadRequest, "item, topic and members required"}
	}
	if !protocol.ValidName(in.Item) || !k.CanRead("items/"+in.Item) {
		return nil, "", &GroupError{http.StatusNotFound, "item not found"}
	}
	for _, m := range in.Members {
		if !protocol.ValidName(m) {
			return nil, "", &GroupError{http.StatusBadRequest, "invalid member name: " + m}
		}
	}
	if bad := strangerMention(in.Mentions, append([]string{k.Name}, in.Members...)); bad != "" {
		return nil, "", &GroupError{http.StatusBadRequest, "mention not a member: " + bad}
	}

	now := s.Now()
	groupID := db.NewGroupID()
	g := protocol.Group{
		ID:        groupID,
		Item:      in.Item,
		Topic:     in.Topic,
		CreatedBy: k.Name,
		Status:    "active",
		CreatedAt: now.UTC().Format("2006-01-02T15:04:05Z"),
		Members:   in.Members,
	}
	if err := s.DB.CreateGroup(g); err != nil {
		return nil, "", err
	}

	// Re-read so the response includes the full member list (creator added automatically).
	created, _ := s.DB.GetGroup(groupID)

	// Post the opening message into the group.
	mentions := expandMentions(in.Mentions, g.Members, k.Name)
	kind := protocol.KindNote
	if len(mentions) > 0 {
		kind = protocol.KindAsk
	}
	// Record the sender's role at send time, so later renames do not rewrite
	// who said what on the squad map.
	fromRole := s.roleFor(k.Name)
	msg := protocol.Message{
		ID:          protocol.NewID(k.Name, now),
		From:        k.Name,
		To:          "group:" + groupID,
		Item:        in.Item,
		Kind:        kind,
		GroupID:     groupID,
		Mentions:    mentions,
		FromRole:    fromRole,
		RequestedBy: requestedBy(k),
		CreatedAt:   now.UTC(),
		Body:        in.Body,
		Task:        in.Topic,
	}
	if err := s.DB.SendGroupMessage(msg, k.Owner); err != nil {
		return nil, "", err
	}

	s.DB.Audit(k.Name, "group.create", groupID+" topic="+in.Topic)
	return created, msg.ID, nil
}

// canSeeGroup is the single access rule for a room: members, plus the owner,
// who already sees every group on the dashboard.
func (s *Server) canSeeGroup(groupID string, k *Key) bool {
	return k.Owner || s.DB.IsGroupMember(groupID, k.Name)
}

// listGroups returns groups the caller belongs to; the owner sees them all.
func (s *Server) listGroups(w http.ResponseWriter, r *http.Request, k *Key) {
	q := r.URL.Query()
	who := k.Name
	if k.Owner {
		who = ""
	}
	groups, err := s.DB.ListGroups(who, q.Get("item"), q.Get("status"))
	if err != nil {
		internal(w, err)
		return
	}
	if groups == nil {
		groups = []protocol.Group{}
	}
	ok(w, groups)
}

// getGroup returns one group's metadata and members.
func (s *Server) getGroup(w http.ResponseWriter, r *http.Request, k *Key) {
	g, err := s.DB.GetGroup(r.PathValue("id"))
	if err != nil {
		internal(w, err)
		return
	}
	if g == nil || !s.canSeeGroup(g.ID, k) {
		fail(w, http.StatusNotFound, "group not found")
		return
	}
	ok(w, g)
}

// listGroupMessages returns messages in a group, oldest first.
func (s *Server) listGroupMessages(w http.ResponseWriter, r *http.Request, k *Key) {
	groupID := r.PathValue("id")
	if !s.canSeeGroup(groupID, k) {
		fail(w, http.StatusNotFound, "group not found")
		return
	}
	limit := 200
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 1000 {
		limit = n
	}
	msgs, err := s.DB.ListGroupMessages(groupID, limit)
	if err != nil {
		internal(w, err)
		return
	}
	type row struct {
		protocol.Message
		Read bool `json:"read"`
	}
	out := make([]row, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, row{m, s.DB.IsRead(k.Name, m.ID)})
	}
	ok(w, out)
}

// postGroupMessage sends a message into an active group.
func (s *Server) postGroupMessage(w http.ResponseWriter, r *http.Request, k *Key) {
	groupID := r.PathValue("id")
	g, err := s.DB.GetGroup(groupID)
	if err != nil {
		internal(w, err)
		return
	}
	if g == nil || !s.canSeeGroup(groupID, k) {
		fail(w, http.StatusNotFound, "group not found")
		return
	}
	if g.Status != "active" {
		fail(w, http.StatusForbidden, "group is closed")
		return
	}

	var in struct {
		Body     string   `json:"body"`
		Mentions []string `json:"mentions"`
		ReplyTo  string   `json:"reply_to"`
		Kind     string   `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	if strings.TrimSpace(in.Body) == "" {
		fail(w, http.StatusBadRequest, "body required")
		return
	}
	// Validate reply_to belongs to the same group.
	if in.ReplyTo != "" {
		orig, err := s.DB.GetMessage(in.ReplyTo)
		if err != nil || orig == nil || orig.GroupID != groupID {
			fail(w, http.StatusBadRequest, "reply_to not in this group")
			return
		}
	}

	if bad := strangerMention(in.Mentions, g.Members); bad != "" {
		fail(w, http.StatusBadRequest, "mention not a member: "+bad)
		return
	}
	mentions := expandMentions(in.Mentions, g.Members, k.Name)
	kind := in.Kind
	if kind == "" {
		kind = protocol.KindNote
		if len(mentions) > 0 {
			kind = protocol.KindAsk
		}
	}
	if kind != protocol.KindAsk && kind != protocol.KindNote && kind != protocol.KindReply {
		fail(w, http.StatusBadRequest, "kind must be ask|note|reply")
		return
	}

	now := s.Now()
	msg := protocol.Message{
		ID:          protocol.NewID(k.Name, now),
		From:        k.Name,
		To:          "group:" + groupID,
		Item:        g.Item,
		Kind:        kind,
		GroupID:     groupID,
		Mentions:    mentions,
		ReplyTo:     in.ReplyTo,
		FromRole:    s.roleFor(k.Name),
		RequestedBy: requestedBy(k),
		CreatedAt:   now.UTC(),
		Body:        in.Body,
		Task:        g.Topic,
	}
	if err := s.DB.SendGroupMessage(msg, k.Owner); err != nil {
		if errors.Is(err, db.ErrGroupChanged) {
			fail(w, 409, err.Error())
			return
		}
		internal(w, err)
		return
	}

	s.DB.Audit(k.Name, "group.send", msg.ID+" → "+groupID)
	writeJSON(w, http.StatusCreated, map[string]any{"id": msg.ID})
}

// ackGroupMessage records a read receipt for one message in a group the
// caller belongs to. Read state stays per identity, so acking is idempotent.
func (s *Server) ackGroupMessage(w http.ResponseWriter, r *http.Request, k *Key) {
	groupID, messageID := r.PathValue("id"), r.PathValue("message")
	if !s.canSeeGroup(groupID, k) {
		fail(w, http.StatusNotFound, "group not found")
		return
	}
	m, err := s.DB.GetMessage(messageID)
	if err != nil {
		internal(w, err)
		return
	}
	if m == nil || m.GroupID != groupID {
		fail(w, http.StatusNotFound, "message not found in this group")
		return
	}
	if err := s.DB.MarkRead(k.Name, messageID); err != nil {
		internal(w, err)
		return
	}
	ok(w, map[string]any{"id": messageID, "read": true})
}

// closeGroup marks a group as closed. Only the creator or owner may close it.
func (s *Server) closeGroup(w http.ResponseWriter, r *http.Request, k *Key) {
	groupID := r.PathValue("id")
	g, err := s.DB.GetGroup(groupID)
	if err != nil {
		internal(w, err)
		return
	}
	if g == nil {
		fail(w, http.StatusNotFound, "group not found")
		return
	}
	if g.CreatedBy != k.Name && !k.Owner {
		fail(w, http.StatusForbidden, "only creator or owner can close a group")
		return
	}
	if err := s.DB.CloseGroup(groupID, k.Name, k.Owner); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	s.DB.Audit(k.Name, "group.close", groupID)
	ok(w, map[string]any{"id": groupID, "status": "closed"})
}

// roleFor reads the published role of a key holder; an unreadable registry
// leaves it empty rather than failing the message.
func (s *Server) roleFor(name string) string {
	if s.Store == nil {
		return ""
	}
	if err := s.Store.Refresh(); err != nil {
		return ""
	}
	reg, err := agents.Load(s.Store)
	if err != nil {
		return ""
	}
	return reg.RoleFor(name)
}

// strangerMention returns the first mention that is neither "all" nor a
// member: waking a non-member would hand it a task in a group it cannot read.
func strangerMention(mentions, members []string) string {
	in := map[string]bool{"all": true}
	for _, m := range members {
		in[m] = true
	}
	for _, m := range mentions {
		if !in[m] {
			return m
		}
	}
	return ""
}

// changeGroupMembers adds and removes members. Only the owner or the group's
// creator may do it: membership decides who can read the room.
func (s *Server) changeGroupMembers(w http.ResponseWriter, r *http.Request, k *Key) {
	var in protocol.GroupMemberInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	g, err := s.DB.GetGroup(r.PathValue("id"))
	if err != nil {
		internal(w, err)
		return
	}
	if g == nil || !s.canSeeGroup(g.ID, k) {
		fail(w, http.StatusNotFound, "group not found")
		return
	}
	updated, added, removed, cancelled, cerr := s.UpdateGroupMembers(k, g, in)
	if ge, isGE := cerr.(*GroupError); isGE {
		fail(w, ge.Status, ge.Msg)
		return
	}
	if cerr != nil {
		internal(w, cerr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"group":             updated,
		"added":             orEmpty(added),
		"removed":           orEmpty(removed),
		"cancelled_wakeups": cancelled,
	})
}

// UpdateGroupMembers is the dashboard's entry point; it shares every rule with
// the API and returns the reloaded group plus what actually changed.
func (s *Server) UpdateGroupMembersAs(name, groupID string, in protocol.GroupMemberInput) (*protocol.Group, []string, []string, int, error) {
	for _, k := range s.Keys.List() {
		if k.Name == name {
			g, err := s.DB.GetGroup(groupID)
			if err != nil {
				return nil, nil, nil, 0, err
			}
			if g == nil {
				return nil, nil, nil, 0, &GroupError{http.StatusNotFound, "group not found"}
			}
			return s.UpdateGroupMembers(&k, g, in)
		}
	}
	return nil, nil, nil, 0, &GroupError{http.StatusForbidden, "unknown caller"}
}

// UpdateGroupMembers applies a member change, records it as a system note and
// cancels the removed members' outstanding tasks in that group.
func (s *Server) UpdateGroupMembers(k *Key, g *protocol.Group, in protocol.GroupMemberInput) (*protocol.Group, []string, []string, int, error) {
	if g.Status != "active" {
		return nil, nil, nil, 0, &GroupError{http.StatusForbidden, "group is closed"}
	}
	if g.CreatedBy != k.Name && !k.Owner {
		return nil, nil, nil, 0, &GroupError{http.StatusForbidden, "only creator or owner can change members"}
	}
	remove := map[string]bool{}
	for _, m := range in.Remove {
		if !protocol.ValidName(m) {
			return nil, nil, nil, 0, &GroupError{http.StatusBadRequest, "invalid member name: " + m}
		}
		remove[m] = true
	}
	var add []string
	seen := map[string]bool{}
	for _, m := range in.Add {
		if !protocol.ValidName(m) {
			return nil, nil, nil, 0, &GroupError{http.StatusBadRequest, "invalid member name: " + m}
		}
		if remove[m] || seen[m] {
			continue
		}
		seen[m] = true
		add = append(add, m)
	}
	if len(add) == 0 && len(remove) == 0 {
		return nil, nil, nil, 0, &GroupError{http.StatusBadRequest, "add or remove required"}
	}

	removeList := make([]string, 0, len(remove))
	for member := range remove {
		removeList = append(removeList, member)
	}
	now := s.Now()
	role := s.roleFor(k.Name)
	makeNote := func(added, removed []string, cancelled int) protocol.Message {
		var lines []string
		if len(added) > 0 {
			lines = append(lines, "加入："+strings.Join(added, "、"))
		}
		if len(removed) > 0 {
			line := "移出：" + strings.Join(removed, "、")
			if cancelled > 0 {
				line += fmt.Sprintf("（已取消 %d 个待处理任务）", cancelled)
			}
			lines = append(lines, line)
		}
		return protocol.Message{ID: protocol.NewID(k.Name, now), From: k.Name, To: "group:" + g.ID, Item: g.Item, Kind: protocol.KindNote, GroupID: g.ID, FromRole: role, RequestedBy: requestedBy(k), CreatedAt: now.UTC(), Task: g.Topic, Body: "成员调整 · " + strings.Join(lines, "；")}
	}
	added, removed, cancelled, err := s.DB.ChangeGroupMembers(g.ID, k.Name, k.Owner, add, removeList, makeNote)
	if errors.Is(err, db.ErrGroupNoChange) || errors.Is(err, db.ErrGroupChanged) {
		return nil, nil, nil, 0, &GroupError{http.StatusConflict, err.Error()}
	}
	if err != nil {
		return nil, nil, nil, 0, err
	}
	s.DB.Audit(k.Name, "group.members", fmt.Sprintf("%s +%v -%v", g.ID, added, removed))

	updated, err := s.DB.GetGroup(g.ID)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	return updated, added, removed, cancelled, nil
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// expandMentions resolves "all" to all group members except the sender.
func expandMentions(mentions []string, members []string, sender string) []string {
	if len(mentions) == 0 {
		return nil
	}
	for _, m := range mentions {
		if m == "all" {
			var out []string
			for _, member := range members {
				if member != sender {
					out = append(out, member)
				}
			}
			return out
		}
	}
	// Filter out sender from mentions.
	var out []string
	for _, m := range mentions {
		if m != sender {
			out = append(out, m)
		}
	}
	return out
}

func requestedBy(k *Key) string {
	if k.Owner {
		return protocol.RequestedByOwner
	}
	return protocol.RequestedByAgent
}

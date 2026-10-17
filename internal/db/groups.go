package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ketchupz1999/agents-home-public/internal/protocol"
)

// CreateGroup inserts a group with its members in one transaction.
// The creator is automatically added to the member list.
func (d *DB) CreateGroup(g protocol.Group) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`INSERT INTO groups(id, item, topic, created_by, status, created_at)
		VALUES(?,?,?,?,?,?)`, g.ID, g.Item, g.Topic, g.CreatedBy, g.Status, g.CreatedAt)
	if err != nil {
		return err
	}

	members := uniqueMembers(g.CreatedBy, g.Members)
	for _, m := range members {
		if _, err := tx.Exec(`INSERT INTO group_members(group_id, agent) VALUES(?,?)`, g.ID, m); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetGroup returns a group by ID with its member list, or nil if not found.
func (d *DB) GetGroup(id string) (*protocol.Group, error) {
	var g protocol.Group
	err := d.QueryRow(`SELECT id, item, topic, created_by, status, closed_at, closed_by, created_at
		FROM groups WHERE id=?`, id).Scan(
		&g.ID, &g.Item, &g.Topic, &g.CreatedBy, &g.Status, &g.ClosedAt, &g.ClosedBy, &g.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	g.Members, err = d.groupMembers(id)
	return &g, err
}

// ListGroups returns groups the agent belongs to, optionally filtered by item.
// Pass agent="" to list all groups (for owner/admin dashboard).
// By default only active groups are returned; pass statusFilter="all" for everything.
func (d *DB) ListGroups(agent, item, statusFilter string) ([]protocol.Group, error) {
	var q string
	var args []any
	if agent == "" {
		q = `SELECT g.id, g.item, g.topic, g.created_by, g.status, g.closed_at, g.closed_by, g.created_at
			FROM groups g WHERE 1=1`
	} else {
		q = `SELECT g.id, g.item, g.topic, g.created_by, g.status, g.closed_at, g.closed_by, g.created_at
			FROM groups g JOIN group_members gm ON g.id=gm.group_id WHERE gm.agent=?`
		args = append(args, agent)
	}
	if item != "" {
		q += " AND g.item=?"
		args = append(args, item)
	}
	if statusFilter != "all" {
		q += " AND g.status='active'"
	}
	q += " ORDER BY g.created_at DESC"

	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Group
	for rows.Next() {
		var g protocol.Group
		if err := rows.Scan(&g.ID, &g.Item, &g.Topic, &g.CreatedBy, &g.Status, &g.ClosedAt, &g.ClosedBy, &g.CreatedAt); err != nil {
			return nil, err
		}
		g.Members, _ = d.groupMembers(g.ID)
		out = append(out, g)
	}
	return out, rows.Err()
}

// IsGroupMember reports whether agent belongs to the group.
func (d *DB) IsGroupMember(groupID, agent string) bool {
	var one int
	return d.QueryRow(`SELECT 1 FROM group_members WHERE group_id=? AND agent=?`, groupID, agent).Scan(&one) == nil
}

// CloseGroup marks a group as closed. Only the creator or owner should call this.
func (d *DB) CloseGroup(id, closedBy string, owner bool) error {
	res, err := d.Exec(`UPDATE groups SET status='closed', closed_at=?, closed_by=? WHERE id=? AND status='active'
		AND (? OR (created_by=? AND EXISTS(SELECT 1 FROM group_members WHERE group_id=groups.id AND agent=?)))`,
		now(), closedBy, id, owner, closedBy, closedBy)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("group not found or already closed")
	}
	return nil
}

// PutGroupMessage inserts a message with group routing.
// The message body is stored as JSON for full-fidelity retrieval.
func (d *DB) PutGroupMessage(m protocol.Message) error {
	return putGroupMessage(d.DB, m)
}

// Both normal sends and transactional wakeup completion use the same encoding.
func putGroupMessage(q interface {
	Exec(string, ...any) (sql.Result, error)
}, m protocol.Message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	mentionsJSON := "[]"
	if len(m.Mentions) > 0 {
		mj, _ := json.Marshal(m.Mentions)
		mentionsJSON = string(mj)
	}
	_, err = q.Exec(`INSERT INTO messages(id, item, from_agent, to_agent, kind, reply_to, requested_by, task, created_at, body, group_id, mentions)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.Item, m.From, m.To, m.Kind, m.ReplyTo, m.RequestedBy, m.Task,
		m.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"), string(b), m.GroupID, mentionsJSON)
	return err
}

// ListGroupMessages returns messages in a group, oldest first.
func (d *DB) ListGroupMessages(groupID string, limit int) ([]protocol.Message, error) {
	q := `SELECT body FROM messages WHERE group_id=? ORDER BY created_at, rowid`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := d.Query(q, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Message
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var m protocol.Message
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AddGroupMembers inserts the members that are not in the group yet and
// returns the ones actually added, so the caller can report a no-op honestly.
func (d *DB) AddGroupMembers(groupID string, agents []string) ([]string, error) {
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var added []string
	for _, a := range agents {
		res, err := tx.Exec(`INSERT OR IGNORE INTO group_members(group_id, agent) VALUES(?,?)`, groupID, a)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added = append(added, a)
		}
	}
	return added, tx.Commit()
}

// RemoveGroupMember drops one member from a group; removing a non-member is a
// no-op rather than an error, so retries are safe.
func (d *DB) RemoveGroupMember(groupID, agent string) (bool, error) {
	res, err := d.Exec(`DELETE FROM group_members WHERE group_id=? AND agent=?`, groupID, agent)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CancelPendingGroupWakeups marks a member's outstanding tasks in one group as
// cancelled: keeping them would ask someone to answer in a room they just left.
func (d *DB) CancelPendingGroupWakeups(groupID, recipient string) (int, error) {
	res, err := d.Exec(`UPDATE wakeups SET status='cancelled'
		WHERE group_id=? AND recipient=? AND status IN ('pending','processing')`, groupID, recipient)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (d *DB) groupMembers(groupID string) ([]string, error) {
	rows, err := d.Query(`SELECT agent FROM group_members WHERE group_id=? ORDER BY agent`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		members = append(members, a)
	}
	return members, rows.Err()
}

// uniqueMembers ensures creator is in the list and deduplicates.
func uniqueMembers(creator string, members []string) []string {
	seen := map[string]bool{creator: true}
	out := []string{creator}
	for _, m := range members {
		if m != "" && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// NewGroupID generates a short random ID for a group.
func NewGroupID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "grp_" + hex.EncodeToString(b)
}

// ListGroupMessagesByItem returns an item's group messages, oldest first.
func (d *DB) ListGroupMessagesByItem(item string, limit int) ([]protocol.Message, error) {
	return d.groupMessageQuery(`SELECT body FROM messages WHERE group_id<>'' AND item=? ORDER BY created_at, rowid`, limit, item)
}

// ListGroupMessagesAll returns every group message, oldest first.
func (d *DB) ListGroupMessagesAll(limit int) ([]protocol.Message, error) {
	return d.groupMessageQuery(`SELECT body FROM messages WHERE group_id<>'' ORDER BY created_at, rowid`, limit)
}

func (d *DB) groupMessageQuery(q string, limit int, args ...any) ([]protocol.Message, error) {
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.Message{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var m protocol.Message
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListUnreadGroupMessages returns the reader's unread group messages.
func (d *DB) ListUnreadGroupMessages(reader string) ([]protocol.Message, error) {
	rows, err := d.Query(`SELECT m.body FROM messages m
		WHERE m.group_id<>'' AND NOT EXISTS(SELECT 1 FROM reads r WHERE r.message_id=m.id AND r.agent=?)
		ORDER BY m.created_at, m.id`, reader)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.Message{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var m protocol.Message
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CountGroupMessages returns the number of messages in a group.
func (d *DB) CountGroupMessages(groupID string) int {
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM messages WHERE group_id=?`, groupID).Scan(&n)
	return n
}

// MentionsAgent checks if mentions list contains the given agent name.
func MentionsAgent(mentionsJSON, agent string) bool {
	if mentionsJSON == "" || mentionsJSON == "[]" {
		return false
	}
	return strings.Contains(mentionsJSON, `"`+agent+`"`)
}

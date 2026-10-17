package db

import (
	"errors"
	"github.com/ketchupz1999/agents-home-public/internal/protocol"
)

var ErrGroupChanged = errors.New("group permissions or state changed; reload before retrying")

// SendGroupMessage validates live permissions under SQLite's writer lock.
// Insertion and all wakeups commit together; a slow body cannot outlive removal
// or closure, and a failed wakeup insertion cannot leave a delivered message.
func (d *DB) SendGroupMessage(m protocol.Message, owner bool) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE groups SET status=status WHERE id=? AND status='active'
  AND (? OR EXISTS(SELECT 1 FROM group_members WHERE group_id=groups.id AND agent=?))`, m.GroupID, owner, m.From)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrGroupChanged
	}
	for _, recipient := range m.Mentions {
		var one int
		if err := tx.QueryRow(`SELECT 1 FROM group_members WHERE group_id=? AND agent=?`, m.GroupID, recipient).Scan(&one); err != nil {
			return ErrGroupChanged
		}
	}
	if m.ReplyTo != "" {
		var one int
		if err := tx.QueryRow(`SELECT 1 FROM messages WHERE id=? AND group_id=?`, m.ReplyTo, m.GroupID).Scan(&one); err != nil {
			return ErrGroupChanged
		}
	}
	if err := putGroupMessage(tx, m); err != nil {
		return err
	}
	if m.Kind == protocol.KindAsk {
		for _, recipient := range m.Mentions {
			if recipient == m.From {
				continue
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO wakeups(id,recipient,message_id,group_id,reason,status,created_at) VALUES(?,?,?,?,?,'pending',?)`, NewWakeupID(), recipient, m.ID, m.GroupID, "mention", m.CreatedAt.UTC().Format("2006-01-02T15:04:05Z")); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

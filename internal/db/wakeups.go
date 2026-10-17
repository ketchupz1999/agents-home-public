package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/ketchupz1999/agents-home-public/internal/protocol"
)

const (
	wakeupClaimTimeout = 180 * time.Second // processing → pending after this
	wakeupMaxAttempts  = 3                 // after this, status becomes failed
)

// CreateWakeup inserts a wakeup task. Duplicate (recipient, message_id) is silently ignored.
func (d *DB) CreateWakeup(w protocol.Wakeup) error {
	_, err := d.Exec(`INSERT OR IGNORE INTO wakeups(id, recipient, message_id, group_id, reason, status, created_at)
		VALUES(?,?,?,?,?,?,?)`,
		w.ID, w.Recipient, w.MessageID, w.GroupID, w.Reason, "pending", w.CreatedAt)
	return err
}

// PollWakeups returns pending tasks for the given recipient.
// Before returning, it expires any claimed tasks that have timed out.
func (d *DB) PollWakeups(recipient string, limit int) ([]protocol.Wakeup, error) {
	// Expire stale claims: processing → pending if claim timed out and under max attempts.
	cutoff := time.Now().UTC().Add(-wakeupClaimTimeout).Format(time.RFC3339)
	d.Exec(`UPDATE wakeups SET status='pending', consumer='', claim_token='', claimed_at=''
		WHERE recipient=? AND status='processing' AND claimed_at < ? AND claimed_at != '' AND attempt < ?`,
		recipient, cutoff, wakeupMaxAttempts)
	// Mark as failed if max attempts reached.
	d.Exec(`UPDATE wakeups SET status='failed'
		WHERE recipient=? AND status='pending' AND attempt >= ?`,
		recipient, wakeupMaxAttempts)

	return d.PendingWakeups(recipient, limit)
}

// PendingWakeups reads the snapshot without expiring claims.
func (d *DB) PendingWakeups(recipient string, limit int) ([]protocol.Wakeup, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.Query(`SELECT id, recipient, message_id, group_id, reason, status, attempt, created_at
		FROM wakeups WHERE recipient=? AND status='pending' ORDER BY created_at, id LIMIT ?`,
		recipient, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Wakeup
	for rows.Next() {
		var w protocol.Wakeup
		if err := rows.Scan(&w.ID, &w.Recipient, &w.MessageID, &w.GroupID, &w.Reason, &w.Status, &w.Attempt, &w.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ClaimWakeup atomically claims a pending task. Returns the updated wakeup with claim_token.
func (d *DB) ClaimWakeup(id, recipient, consumer string) (*protocol.Wakeup, error) {
	token := newClaimToken()
	ts := now()
	res, err := d.Exec(`UPDATE wakeups SET status='processing', consumer=?, claim_token=?, claimed_at=?, attempt=attempt+1
		WHERE id=? AND recipient=? AND status='pending'`,
		consumer, token, ts, id, recipient)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("wakeup not claimable: not found, wrong recipient, or not pending")
	}
	return d.GetWakeup(id)
}

// FinishWakeup validates the caller and claim, then atomically completes the
// task, inserts its reply, and acknowledges the original. The guarded UPDATE
// obtains SQLite's write lock before any side effect or membership check can race.
func (d *DB) FinishWakeup(id, recipient, claimToken, result, errorSummary string, owner bool, reply *protocol.Message) error {
	if claimToken == "" || (result != "replied" && result != "failed") || (result == "replied") != (reply != nil) {
		return fmt.Errorf("invalid completion")
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	replyID := ""
	if reply != nil {
		replyID = reply.ID
	}
	res, err := tx.Exec(`UPDATE wakeups SET status=?, finished_at=?, result_message_id=?, error_summary=?
		WHERE id=? AND recipient=? AND claim_token=? AND status='processing'
		AND EXISTS(SELECT 1 FROM groups g WHERE g.id=wakeups.group_id AND g.status='active'
		AND (? OR EXISTS(SELECT 1 FROM group_members gm WHERE gm.group_id=g.id AND gm.agent=?)))`,
		result, now(), replyID, errorSummary, id, recipient, claimToken, owner, recipient)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("wakeup not finishable")
	}
	if reply != nil {
		var groupID, messageID string
		if err := tx.QueryRow(`SELECT group_id, message_id FROM wakeups WHERE id=?`, id).Scan(&groupID, &messageID); err != nil {
			return err
		}
		if reply.From != recipient || reply.GroupID != groupID || reply.ReplyTo != messageID {
			return fmt.Errorf("reply does not match task")
		}
		if err := putGroupMessage(tx, *reply); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO reads(agent,message_id,read_at) VALUES(?,?,?)`, recipient, messageID, now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetWakeup returns a wakeup by ID.
func (d *DB) GetWakeup(id string) (*protocol.Wakeup, error) {
	var w protocol.Wakeup
	err := d.QueryRow(`SELECT id, recipient, message_id, group_id, reason, status, consumer, claim_token, attempt,
		created_at, claimed_at, finished_at, result_message_id, error_summary
		FROM wakeups WHERE id=?`, id).Scan(
		&w.ID, &w.Recipient, &w.MessageID, &w.GroupID, &w.Reason, &w.Status, &w.Consumer, &w.ClaimToken, &w.Attempt,
		&w.CreatedAt, &w.ClaimedAt, &w.FinishedAt, &w.ResultMessageID, &w.ErrorSummary)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}

func newClaimToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// NewWakeupID generates a random wakeup ID.
func NewWakeupID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "wake_" + hex.EncodeToString(b)
}

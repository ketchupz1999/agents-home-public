// Package db stores what is in flight: mailbox messages, who read what,
// branch decisions and an audit trail. Slow-moving content lives in git
// instead; nothing here is the source of truth for files.
package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ketchupz1999/agents-home-public/internal/protocol"

	_ "modernc.org/sqlite"
)

type DB struct{ *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS actions (
 id TEXT PRIMARY KEY,
 created_by TEXT NOT NULL,
 client_id TEXT NOT NULL,
 item TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('suggested','planned','done','dismissed')),
 revision INTEGER NOT NULL CHECK(revision>0),
 request_fingerprint TEXT NOT NULL,
 body TEXT NOT NULL,
 UNIQUE(created_by,client_id)
);
CREATE INDEX IF NOT EXISTS idx_actions_item_status ON actions(item,status);

CREATE TABLE IF NOT EXISTS agent_runs (
  principal_id TEXT NOT NULL,
  client_run_id TEXT NOT NULL,
  harness TEXT NOT NULL,
  workspace TEXT NOT NULL DEFAULT '',
  device_id TEXT NOT NULL DEFAULT '',
  first_seen_at TEXT NOT NULL,
  last_seen_at TEXT,
  PRIMARY KEY(principal_id, client_run_id)
);
CREATE INDEX IF NOT EXISTS idx_runs_seen ON agent_runs(principal_id, last_seen_at);
CREATE TABLE IF NOT EXISTS agent_observations (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  principal_id TEXT NOT NULL,
  run_id TEXT,
  kind TEXT NOT NULL,
  occurred_at TEXT NOT NULL,
  resource TEXT NOT NULL,
  content_sha TEXT NOT NULL DEFAULT '',
  metadata_json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_observations_principal ON agent_observations(principal_id, occurred_at, id);
CREATE INDEX IF NOT EXISTS idx_observations_kind ON agent_observations(kind, occurred_at, id);

CREATE TABLE IF NOT EXISTS focus (
  id INTEGER PRIMARY KEY CHECK(id=1),
  items TEXT NOT NULL,
  revision INTEGER NOT NULL CHECK(revision>0)
);

CREATE TABLE IF NOT EXISTS messages (
  id            TEXT PRIMARY KEY,
  item          TEXT NOT NULL,
  from_agent    TEXT NOT NULL,
  to_agent      TEXT NOT NULL,
  kind          TEXT NOT NULL,
  reply_to      TEXT NOT NULL DEFAULT '',
  requested_by  TEXT NOT NULL,
  task          TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL,
  body          TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_to   ON messages(to_agent, created_at);
CREATE INDEX IF NOT EXISTS idx_messages_item ON messages(item, created_at);

CREATE TABLE IF NOT EXISTS reads (
  agent      TEXT NOT NULL,
  message_id TEXT NOT NULL,
  read_at    TEXT NOT NULL,
  PRIMARY KEY (agent, message_id)
);

CREATE TABLE IF NOT EXISTS branch_decisions (
  name       TEXT PRIMARY KEY,
  agent      TEXT NOT NULL,
  topic      TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL,
  created_at TEXT NOT NULL,
  decided_at TEXT NOT NULL DEFAULT '',
  decided_by TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS audit (
  id     INTEGER PRIMARY KEY AUTOINCREMENT,
  at     TEXT NOT NULL,
  agent  TEXT NOT NULL,
  action TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT ''
);

-- Discussion groups: one topic per group, members fixed at creation.
CREATE TABLE IF NOT EXISTS groups (
  id         TEXT PRIMARY KEY,
  item       TEXT NOT NULL,
  topic      TEXT NOT NULL,
  created_by TEXT NOT NULL,
  status     TEXT NOT NULL DEFAULT 'active',  -- active | closed
  closed_at  TEXT NOT NULL DEFAULT '',
  closed_by  TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_groups_item ON groups(item);

-- Group membership: created with the group; the owner or creator may add or
-- remove members later through POST /v1/groups/{id}/members.
CREATE TABLE IF NOT EXISTS group_members (
  group_id TEXT NOT NULL,
  agent    TEXT NOT NULL,
  PRIMARY KEY (group_id, agent)
);

-- Wakeup tasks: auto-created on ask/@mention, claimed by local receivers.
CREATE TABLE IF NOT EXISTS wakeups (
  id                TEXT PRIMARY KEY,
  recipient         TEXT NOT NULL,
  message_id        TEXT NOT NULL,
  group_id          TEXT NOT NULL DEFAULT '',
  reason            TEXT NOT NULL,              -- mention | ask
  status            TEXT NOT NULL DEFAULT 'pending',
  consumer          TEXT NOT NULL DEFAULT '',
  claim_token       TEXT NOT NULL DEFAULT '',
  attempt           INTEGER NOT NULL DEFAULT 0,
  created_at        TEXT NOT NULL,
  claimed_at        TEXT NOT NULL DEFAULT '',
  finished_at       TEXT NOT NULL DEFAULT '',
  result_message_id TEXT NOT NULL DEFAULT '',
  error_summary     TEXT NOT NULL DEFAULT '',
  UNIQUE(recipient, message_id)
);
CREATE INDEX IF NOT EXISTS idx_wakeups_recipient ON wakeups(recipient, status);`

func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	sqldb, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	if _, err := sqldb.Exec(schema); err != nil {
		return nil, err
	}
	// Extend messages for group support: add columns if they don't exist yet.
	if err := addColumnIfMissing(sqldb, "messages", "group_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return nil, err
	}
	if err := addColumnIfMissing(sqldb, "messages", "mentions", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return nil, err
	}
	return &DB{sqldb}, nil
}

// addColumnIfMissing adds a column to a table only when it is not already present.
func addColumnIfMissing(db *sql.DB, table, column, typedef string) error {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ, dflt sql.NullString
		var notnull, pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name.String == column {
			return nil
		}
	}
	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, typedef))
	return err
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func (d *DB) Audit(agent, action, detail string) {
	_, _ = d.Exec(`INSERT INTO audit(at, agent, action, detail) VALUES(?,?,?,?)`, now(), agent, action, detail)
}

// ---- messages ----

func (d *DB) PutMessage(m protocol.Message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = d.Exec(`INSERT INTO messages(id,item,from_agent,to_agent,kind,reply_to,requested_by,task,created_at,body)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.Item, m.From, m.To, m.Kind, m.ReplyTo, m.RequestedBy, m.Task,
		m.CreatedAt.UTC().Format(time.RFC3339), string(b))
	return err
}

// ListMessages returns messages oldest first.
// to: "" or "all" means anyone; item filters by item; unread filters by reader.
func (d *DB) ListMessages(reader, to, item string, unread bool, limit int) ([]protocol.Message, error) {
	q := `SELECT m.body FROM messages m`
	var args []any
	if unread {
		q += ` WHERE NOT EXISTS(SELECT 1 FROM reads r WHERE r.message_id=m.id AND r.agent=?)`
		args = append(args, reader)
	}
	var cond []string
	if to != "" && to != "all" {
		cond = append(cond, "m.to_agent=?")
		args = append(args, to)
	}
	if item != "" {
		cond = append(cond, "m.item=?")
		args = append(args, item)
	}
	for _, c := range cond {
		if unread {
			q += " AND " + c
		} else {
			q += " WHERE " + c
			unread = true // only the first condition needs WHERE
		}
	}
	q += " ORDER BY m.created_at, m.id"
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

func (d *DB) GetMessage(id string) (*protocol.Message, error) {
	var raw string
	err := d.QueryRow(`SELECT body FROM messages WHERE id=?`, id).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m protocol.Message
	return &m, json.Unmarshal([]byte(raw), &m)
}

func (d *DB) IsRead(agent, id string) bool {
	var one int
	err := d.QueryRow(`SELECT 1 FROM reads WHERE agent=? AND message_id=?`, agent, id).Scan(&one)
	return err == nil
}

func (d *DB) MarkRead(agent, id string) error {
	_, err := d.Exec(`INSERT OR IGNORE INTO reads(agent, message_id, read_at) VALUES(?,?,?)`, agent, id, now())
	return err
}

// ---- branches ----

type BranchDecision struct {
	Name      string `json:"name"`
	Agent     string `json:"agent"`
	Topic     string `json:"topic"`
	Status    string `json:"status"` // applied | rejected | conflict
	CreatedAt string `json:"created_at"`
	DecidedAt string `json:"decided_at,omitempty"`
	DecidedBy string `json:"decided_by,omitempty"`
}

func (d *DB) RecordBranch(b BranchDecision) error {
	_, err := d.Exec(`INSERT INTO branch_decisions(name,agent,topic,status,created_at,decided_at,decided_by)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET status=excluded.status, decided_at=excluded.decided_at, decided_by=excluded.decided_by`,
		b.Name, b.Agent, b.Topic, b.Status, b.CreatedAt, b.DecidedAt, b.DecidedBy)
	return err
}

func (d *DB) BranchStatus(name string) string {
	var s string
	if err := d.QueryRow(`SELECT status FROM branch_decisions WHERE name=?`, name).Scan(&s); err != nil {
		return ""
	}
	return s
}

func (d *DB) RecentAudit(limit int) ([]map[string]string, error) {
	rows, err := d.Query(`SELECT at, agent, action, detail FROM audit ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var at, agent, action, detail string
		if err := rows.Scan(&at, &agent, &action, &detail); err != nil {
			return nil, err
		}
		out = append(out, map[string]string{"at": at, "agent": agent, "action": action, "detail": detail})
	}
	return out, rows.Err()
}

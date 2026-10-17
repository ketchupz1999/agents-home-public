package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ketchupz1999/agents-home-public/internal/protocol"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const TimestampFormat = "2006-01-02T15:04:05.000000000Z"

var ErrRunConflict = errors.New("run attributes conflict")
var ErrRunNotFound = errors.New("run not found")
var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,80}$`)

type Run struct {
	PrincipalID string `json:"principal_id"`
	ClientRunID string `json:"client_run_id"`
	Harness     string `json:"harness"`
	Workspace   string `json:"workspace,omitempty"`
	DeviceID    string `json:"device_id,omitempty"`
	FirstSeenAt string `json:"first_seen_at"`
	LastSeenAt  string `json:"last_seen_at,omitempty"`
}

// ValidRunID 检查客户端运行 ID。
func ValidRunID(id string) bool { return runIDPattern.MatchString(id) }

// ValidateRun 检查声明字段，不据其授予权限。
func ValidateRun(r Run) error {
	if !ValidRunID(r.ClientRunID) || strings.TrimSpace(r.Harness) == "" || len(r.Harness) > 64 || len(r.Workspace) > 256 || len(r.DeviceID) > 80 {
		return fmt.Errorf("invalid run declaration")
	}
	for _, s := range []string{r.Harness, r.Workspace, r.DeviceID} {
		for _, c := range s {
			if unicode.IsControl(c) {
				return fmt.Errorf("control character in run declaration")
			}
		}
	}
	return nil
}

// RegisterRun 幂等登记；不同声明不能覆盖已有运行。
func (d *DB) RegisterRun(r Run) (*Run, error) {
	if err := ValidateRun(r); err != nil {
		return nil, err
	}
	_, err := d.Exec(`INSERT INTO agent_runs(principal_id,client_run_id,harness,workspace,device_id,first_seen_at) VALUES(?,?,?,?,?,?) ON CONFLICT(principal_id,client_run_id) DO NOTHING`, r.PrincipalID, r.ClientRunID, r.Harness, r.Workspace, r.DeviceID, time.Now().UTC().Format(TimestampFormat))
	if err != nil {
		return nil, err
	}
	saved, err := d.GetRun(r.PrincipalID, r.ClientRunID)
	if err != nil {
		return nil, err
	}
	if saved.Harness != r.Harness || saved.Workspace != r.Workspace || saved.DeviceID != r.DeviceID {
		return nil, ErrRunConflict
	}
	return saved, nil
}

// GetRun 只按认证身份和 ID 联合读取。
func (d *DB) GetRun(principal, id string) (*Run, error) {
	var r Run
	err := d.QueryRow(`SELECT principal_id,client_run_id,harness,workspace,device_id,first_seen_at,COALESCE(last_seen_at,'') FROM agent_runs WHERE principal_id=? AND client_run_id=?`, principal, id).Scan(&r.PrincipalID, &r.ClientRunID, &r.Harness, &r.Workspace, &r.DeviceID, &r.FirstSeenAt, &r.LastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRunNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ListRuns 返回指定身份的运行；空过滤表示所有身份，仅供 owner 聚合。
func (d *DB) ListRuns(principals []string) ([]Run, error) {
	q := `SELECT principal_id,client_run_id,harness,workspace,device_id,first_seen_at,COALESCE(last_seen_at,'') FROM agent_runs WHERE 1=1`
	var args []any
	q, args = filterPrincipals(q, args, principals)
	rows, err := d.Query(q+` ORDER BY COALESCE(last_seen_at,first_seen_at) DESC,principal_id,client_run_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var r Run
		if err = rows.Scan(&r.PrincipalID, &r.ClientRunID, &r.Harness, &r.Workspace, &r.DeviceID, &r.FirstSeenAt, &r.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func touchRun(tx *sql.Tx, principal, id, at string) error {
	if id == "" {
		return nil
	}
	result, err := tx.Exec(`UPDATE agent_runs SET last_seen_at=CASE WHEN last_seen_at IS NULL OR last_seen_at<? THEN ? ELSE last_seen_at END WHERE principal_id=? AND client_run_id=?`, at, at, principal, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrRunNotFound
	}
	return nil
}

// PutMessageWithRun 将信件与运行最近出现时间作为单个事务保存。
func (d *DB) PutMessageWithRun(m protocol.Message, runID string) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO messages(id,item,from_agent,to_agent,kind,reply_to,requested_by,task,created_at,body) VALUES(?,?,?,?,?,?,?,?,?,?)`, m.ID, m.Item, m.From, m.To, m.Kind, m.ReplyTo, m.RequestedBy, m.Task, m.CreatedAt.UTC().Format(time.RFC3339), string(b))
	if err != nil {
		return err
	}
	if err = touchRun(tx, m.From, runID, m.CreatedAt.UTC().Format(TimestampFormat)); err != nil {
		return err
	}
	return tx.Commit()
}

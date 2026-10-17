package db

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Observation struct {
	ID           int64  `json:"id"`
	PrincipalID  string `json:"principal_id"`
	RunID        string `json:"run_id,omitempty"`
	Kind         string `json:"kind"`
	OccurredAt   string `json:"occurred_at"`
	Resource     string `json:"resource"`
	ContentSHA   string `json:"content_sha,omitempty"`
	MetadataJSON string `json:"metadata_json"`
}
type ObservationFilter struct {
	Principals   []string
	Kind         string
	Since, Until time.Time
	BeforeAt     string
	BeforeID     int64
	Limit        int
}

// RecordObservation 原子保存观测和 run.last_seen。
func (d *DB) RecordObservation(o Observation) error {
	if o.Kind != "file.served" && o.Kind != "inbox.listed" {
		return fmt.Errorf("invalid observation kind")
	}
	if !json.Valid([]byte(o.MetadataJSON)) {
		return fmt.Errorf("invalid observation metadata")
	}
	if o.OccurredAt == "" {
		o.OccurredAt = time.Now().UTC().Format(TimestampFormat)
	}
	at, err := time.Parse(time.RFC3339Nano, o.OccurredAt)
	if err != nil {
		return err
	}
	o.OccurredAt = at.UTC().Format(TimestampFormat)
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO agent_observations(principal_id,run_id,kind,occurred_at,resource,content_sha,metadata_json) VALUES(?,NULLIF(?,''),?,?,?,?,?)`, o.PrincipalID, o.RunID, o.Kind, o.OccurredAt, o.Resource, o.ContentSHA, o.MetadataJSON)
	if err != nil {
		return err
	}
	if err = touchRun(tx, o.PrincipalID, o.RunID, o.OccurredAt); err != nil {
		return err
	}
	return tx.Commit()
}
func filterPrincipals(q string, args []any, principals []string) (string, []any) {
	if len(principals) > 0 {
		q += ` AND principal_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(principals)), ",") + `)`
		for _, p := range principals {
			args = append(args, p)
		}
	}
	return q, args
}

// ListObservations 按时间和自增 ID 稳定降序分页，每页最多 100 条。
func (d *DB) ListObservations(f ObservationFilter) ([]Observation, error) {
	q := `SELECT id,principal_id,COALESCE(run_id,''),kind,occurred_at,resource,content_sha,metadata_json FROM agent_observations WHERE 1=1`
	var args []any
	q, args = filterPrincipals(q, args, f.Principals)
	if f.Kind != "" {
		q += ` AND kind=?`
		args = append(args, f.Kind)
	}
	if !f.Since.IsZero() {
		q += ` AND occurred_at>=?`
		args = append(args, f.Since.UTC().Format(TimestampFormat))
	}
	if !f.Until.IsZero() {
		q += ` AND occurred_at<?`
		args = append(args, f.Until.UTC().Format(TimestampFormat))
	}
	if f.BeforeAt != "" {
		at, err := time.Parse(time.RFC3339Nano, f.BeforeAt)
		if err != nil || f.BeforeID <= 0 {
			return nil, fmt.Errorf("invalid observation cursor")
		}
		f.BeforeAt = at.UTC().Format(TimestampFormat)
		q += ` AND (occurred_at<? OR (occurred_at=? AND id<?))`
		args = append(args, f.BeforeAt, f.BeforeAt, f.BeforeID)
	}
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 100
	}
	q += ` ORDER BY occurred_at DESC,id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Observation{}
	for rows.Next() {
		var o Observation
		if err = rows.Scan(&o.ID, &o.PrincipalID, &o.RunID, &o.Kind, &o.OccurredAt, &o.Resource, &o.ContentSHA, &o.MetadataJSON); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

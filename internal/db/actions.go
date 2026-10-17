package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ketchupz1999/agents-home-public/internal/protocol"
)

var (
	ErrActionValidation = errors.New("invalid action")
	ErrActionConflict   = errors.New("action changed")
	ErrActionForbidden  = errors.New("owner required")
	ErrActionNotFound   = errors.New("action not found")
)

// ActionProposal 是客户端建议；身份与来源内容指纹由调用方验证后提供。
type ActionProposal struct {
	ClientID        string `json:"client_id"`
	Item            string `json:"item"`
	Title           string `json:"title"`
	Details         string `json:"details"`
	PlanKind        string `json:"plan_kind"`
	PlanDate        string `json:"plan_date"`
	SourceMessageID string `json:"source_message_id,omitempty"`
	SourcePath      string `json:"source_path,omitempty"`
}

// Action 保存当前状态和创建时的身份快照。
type Action struct {
	ActionProposal
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	SourceSHA   string            `json:"source_sha,omitempty"`
	CreatedBy   string            `json:"created_by"`
	CreatedRole string            `json:"created_role,omitempty"`
	Runtime     *protocol.Runtime `json:"runtime,omitempty"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
	CompletedAt string            `json:"completed_at,omitempty"`
	Revision    int64             `json:"revision"`
}

// ActionActor 只能由认证层构造；运行声明不参与权限判断。
type ActionActor struct {
	Name    string
	Role    string
	Owner   bool
	Runtime *protocol.Runtime
}

// ActionUpdate 描述 owner 的单次状态变更及所见版本。
type ActionUpdate struct {
	Operation string
	Revision  int64
	PlanKind  string
	PlanDate  string
}

// ValidateActionPlan 验证日期；周计划必须使用周一，避免客户端分组不一致。
func ValidateActionPlan(kind, date string) error {
	if kind == "later" && date == "" {
		return nil
	}
	if kind != "day" && kind != "week" {
		return fmt.Errorf("%w: plan_kind must be day, week or later", ErrActionValidation)
	}
	t, err := time.Parse("2006-01-02", date)
	if err != nil || (kind == "week" && t.Weekday() != time.Monday) {
		return fmt.Errorf("%w: date must be YYYY-MM-DD (Monday for week)", ErrActionValidation)
	}
	return nil
}

// ValidateActionProposal 验证建议字段，不代替调用方的事项与来源授权检查。
func ValidateActionProposal(p ActionProposal) error {
	if strings.TrimSpace(p.ClientID) == "" || len(p.ClientID) > 128 || !protocol.ValidName(p.Item) || strings.TrimSpace(p.Title) == "" || len(p.Title) > 500 || len(p.Details) > 65536 || (p.SourceMessageID == "") == (p.SourcePath == "") {
		return fmt.Errorf("%w: client_id, item, title and exactly one source required", ErrActionValidation)
	}
	return ValidateActionPlan(p.PlanKind, p.PlanDate)
}

type actionScanner interface{ Scan(...any) error }

func actionFingerprint(p ActionProposal) string {
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// RetryAction 读取原提案的当前结果，不要求其来源文件仍存在。
func (d *DB) RetryAction(p ActionProposal, principal string) (*Action, error) {
	var fingerprint, body string
	err := d.QueryRow(`SELECT request_fingerprint,body FROM actions WHERE created_by=? AND client_id=?`, principal, p.ClientID).Scan(&fingerprint, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if fingerprint != actionFingerprint(p) {
		return nil, ErrActionConflict
	}
	var a Action
	if err = json.Unmarshal([]byte(body), &a); err != nil {
		return nil, err
	}
	return &a, nil
}

func scanAction(row actionScanner) (*Action, error) {
	var raw string
	if err := row.Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	var a Action
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// CreateAction 原始建议指纹独立于可变状态；重试不会撤销 owner 的安排。
func (d *DB) CreateAction(p ActionProposal, actor ActionActor, sourceSHA string) (*Action, bool, error) {
	if err := ValidateActionProposal(p); err != nil {
		return nil, false, err
	}
	if !protocol.ValidName(actor.Name) {
		return nil, false, ErrActionValidation
	}
	fingerprint := actionFingerprint(p)
	stamp := now()
	a := Action{ActionProposal: p, ID: protocol.NewID(actor.Name, time.Now()), Status: "suggested", SourceSHA: sourceSHA, CreatedBy: actor.Name, CreatedRole: actor.Role, Runtime: actor.Runtime, CreatedAt: stamp, UpdatedAt: stamp, Revision: 1}
	body, _ := json.Marshal(a)
	tx, err := d.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`INSERT INTO actions(id,created_by,client_id,item,status,revision,request_fingerprint,body) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(created_by,client_id) DO NOTHING`, a.ID, actor.Name, p.ClientID, p.Item, a.Status, a.Revision, fingerprint, string(body))
	if err != nil {
		return nil, false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if n == 0 {
		var stored, payload string
		if err = tx.QueryRow(`SELECT request_fingerprint,body FROM actions WHERE created_by=? AND client_id=?`, actor.Name, p.ClientID).Scan(&stored, &payload); err != nil {
			return nil, false, err
		}
		if stored != fingerprint {
			return nil, false, ErrActionConflict
		}
		if err = json.Unmarshal([]byte(payload), &a); err != nil {
			return nil, false, err
		}
	} else if err = actionAudit(tx, actor.Name, "action.propose", a); err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	return &a, n == 1, nil
}

// GetAction 读取持久状态；对外接口必须另行验证事项及来源权限。
func (d *DB) GetAction(id string) (*Action, error) {
	return scanAction(d.QueryRow(`SELECT body FROM actions WHERE id=?`, id))
}

// ListActions 返回持久行动，空过滤器表示全部；调用方负责可见性过滤。
func (d *DB) ListActions(item, status string) ([]Action, error) {
	rows, err := d.Query(`SELECT body FROM actions WHERE (?='' OR item=?) AND (?='' OR status=?) ORDER BY id DESC`, item, item, status, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Action{}
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// UpdateAction 在同一写事务内检查版本、执行状态迁移并记录审计。
func (d *DB) UpdateAction(id string, actor ActionActor, u ActionUpdate) (*Action, error) {
	if !actor.Owner {
		return nil, ErrActionForbidden
	}
	if !protocol.ValidName(actor.Name) || u.Revision < 1 {
		return nil, ErrActionValidation
	}
	if u.Operation == "schedule" {
		if err := ValidateActionPlan(u.PlanKind, u.PlanDate); err != nil {
			return nil, err
		}
	} else if u.PlanKind != "" || u.PlanDate != "" {
		return nil, ErrActionValidation
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// 先取得写锁，再读版本，避免并发事务升级读快照导致 SQLITE_BUSY。
	if _, err = tx.Exec(`UPDATE actions SET revision=revision WHERE id=?`, id); err != nil {
		return nil, err
	}
	a, err := scanAction(tx.QueryRow(`SELECT body FROM actions WHERE id=?`, id))
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, ErrActionNotFound
	}
	same := false
	switch u.Operation {
	case "schedule":
		same = a.Status == "planned" && a.PlanKind == u.PlanKind && a.PlanDate == u.PlanDate
	case "complete":
		same = a.Status == "done"
	case "reopen":
		same = a.Status == "planned"
	case "dismiss":
		same = a.Status == "dismissed"
	default:
		return nil, ErrActionValidation
	}
	// 同结果重试可跨版本返回；不同修改必须匹配原版本。
	if same {
		return a, tx.Commit()
	}
	if a.Revision != u.Revision {
		return nil, ErrActionConflict
	}
	switch u.Operation {
	case "schedule":
		if a.Status != "suggested" && a.Status != "planned" {
			return nil, ErrActionValidation
		}
		a.Status = "planned"
		a.PlanKind = u.PlanKind
		a.PlanDate = u.PlanDate
	case "complete":
		if a.Status != "planned" {
			return nil, ErrActionValidation
		}
		a.Status = "done"
		a.CompletedAt = now()
	case "reopen":
		if a.Status != "done" {
			return nil, ErrActionValidation
		}
		a.Status = "planned"
		a.CompletedAt = ""
	case "dismiss":
		if a.Status != "suggested" {
			return nil, ErrActionValidation
		}
		a.Status = "dismissed"
	}
	a.Revision++
	a.UpdatedAt = now()
	body, _ := json.Marshal(a)
	if _, err = tx.Exec(`UPDATE actions SET status=?,revision=?,body=? WHERE id=?`, a.Status, a.Revision, string(body), id); err != nil {
		return nil, err
	}
	if err = actionAudit(tx, actor.Name, "action."+u.Operation, *a); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return a, nil
}

func actionAudit(tx *sql.Tx, actor, operation string, a Action) error {
	raw, _ := json.Marshal(a)
	_, err := tx.Exec(`INSERT INTO audit(at,agent,action,detail) VALUES(?,?,?,?)`, now(), actor, operation, string(raw))
	return err
}

// ActionAudit 包含一次变更后的完整快照。
type ActionAudit struct {
	At        string `json:"at"`
	Actor     string `json:"actor"`
	Operation string `json:"operation"`
	Action    Action `json:"action"`
}

// ActionHistory 只返回指定行动的审计；调用方先检查该行动的读取权限。
func (d *DB) ActionHistory(id string) ([]ActionAudit, error) {
	rows, err := d.Query(`SELECT at,agent,action,detail FROM audit WHERE action LIKE 'action.%' AND json_extract(detail,'$.id')=? ORDER BY audit.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ActionAudit{}
	for rows.Next() {
		var h ActionAudit
		var raw string
		if err = rows.Scan(&h.At, &h.Actor, &h.Operation, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &h.Action); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Package protocol defines the on-disk formats shared by server and clients:
// inbox messages (stored in SQLite) and the Markdown front-matter helpers.
package protocol

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	KindAsk   = "ask"
	KindReply = "reply"
	KindNote  = "note"

	RequestedByOwner = "owner"
	RequestedByAgent = "agent"
)

// Runtime 保存消息发出时的运行声明快照，来源不是权限证明。
type Runtime struct {
	RunID     string `json:"run_id" yaml:"run_id"`
	Harness   string `json:"harness" yaml:"harness"`
	Workspace string `json:"workspace,omitempty" yaml:"workspace,omitempty"`
	DeviceID  string `json:"device_id,omitempty" yaml:"device_id,omitempty"`
	Source    string `json:"source" yaml:"source"`
}

// Message 保存原始信箱身份与可选的角色、运行来源。
type Message struct {
	FromRole    string    `json:"from_role,omitempty" yaml:"from_role,omitempty"`
	Runtime     *Runtime  `json:"runtime,omitempty" yaml:"runtime,omitempty"`
	ID          string    `yaml:"id" json:"id"`
	From        string    `yaml:"from" json:"from"`
	To          string    `yaml:"to" json:"to"`
	Item        string    `yaml:"item" json:"item"`
	Kind        string    `yaml:"kind" json:"kind"`
	ReplyTo     string    `yaml:"reply_to,omitempty" json:"reply_to,omitempty"`
	RequestedBy string    `yaml:"requested_by" json:"requested_by"`
	CreatedAt   time.Time `yaml:"created_at" json:"created_at"`

	GroupID  string   `yaml:"group_id,omitempty" json:"group_id,omitempty"`
	Mentions []string `yaml:"mentions,omitempty" json:"mentions,omitempty"`

	Task               string   `yaml:"task" json:"task"`
	ContextRefs        []string `yaml:"context_refs,omitempty" json:"context_refs,omitempty"`
	Constraints        []string `yaml:"constraints,omitempty" json:"constraints,omitempty"`
	SuccessCriteria    string   `yaml:"success_criteria,omitempty" json:"success_criteria,omitempty"`
	ReturnFormat       string   `yaml:"return_format,omitempty" json:"return_format,omitempty"`
	NeedsOwnerApproval bool     `yaml:"needs_owner_approval" json:"needs_owner_approval"`

	Body string `yaml:"-" json:"body"`
}

// Group is a topic-scoped discussion among a fixed set of agents.
type Group struct {
	ID        string   `json:"id"`
	Item      string   `json:"item"`
	Topic     string   `json:"topic"`
	CreatedBy string   `json:"created_by"`
	Status    string   `json:"status"` // active | closed
	ClosedAt  string   `json:"closed_at,omitempty"`
	ClosedBy  string   `json:"closed_by,omitempty"`
	CreatedAt string   `json:"created_at"`
	Members   []string `json:"members"`
}

// GroupInput is what a creator supplies when opening a group. Mentions may
// be ["all"], which expands to every member except the sender.
type GroupInput struct {
	Item     string   `json:"item"`
	Topic    string   `json:"topic"`
	Members  []string `json:"members"`
	Body     string   `json:"body"`
	Mentions []string `json:"mentions"`
}

// GroupMemberInput changes a group's member list. Remove wins over add when a
// name appears in both, so a caller cannot "remove" by re-adding by accident.
type GroupMemberInput struct {
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}

// Wakeup is a pending task for a local agent to pick up and process.
type Wakeup struct {
	ID              string `json:"id"`
	Recipient       string `json:"recipient"`
	MessageID       string `json:"message_id"`
	GroupID         string `json:"group_id,omitempty"`
	Reason          string `json:"reason"` // mention | ask
	Status          string `json:"status"` // pending | processing | replied | failed
	Consumer        string `json:"consumer,omitempty"`
	Attempt         int    `json:"attempt"`
	CreatedAt       string `json:"created_at"`
	ClaimedAt       string `json:"claimed_at,omitempty"`
	FinishedAt      string `json:"finished_at,omitempty"`
	ResultMessageID string `json:"result_message_id,omitempty"`
	ErrorSummary    string `json:"error_summary,omitempty"`
	ClaimToken      string `json:"claim_token,omitempty"`
}

// Proposal is a whole-file replacement awaiting owner approval.
type Proposal struct {
	ID        string    `yaml:"id" json:"id"`
	From      string    `yaml:"from" json:"from"`
	Target    string    `yaml:"target" json:"target"`
	BaseSHA   string    `yaml:"base_sha" json:"base_sha"`
	Summary   string    `yaml:"summary" json:"summary"`
	Status    string    `yaml:"status" json:"status"` // pending | applied | rejected | stale
	CreatedAt time.Time `yaml:"created_at" json:"created_at"`
	DecidedAt time.Time `yaml:"decided_at,omitempty" json:"decided_at,omitempty"`
	DecidedBy string    `yaml:"decided_by,omitempty" json:"decided_by,omitempty"`

	Content string `yaml:"-" json:"content"`
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// ValidName reports whether s is usable as an agent name or item id.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// NewID returns a sortable, collision-resistant id such as
// 20261009T144200Z-claude-code-a1b2c3.
func NewID(from string, now time.Time) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s-%s", now.UTC().Format("20060102T150405Z"), from, hex.EncodeToString(b))
}

const fence = "---\n"

// Marshal renders front matter followed by body.
func Marshal(meta any, body string) ([]byte, error) {
	y, err := yaml.Marshal(meta)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString(fence)
	buf.Write(y)
	buf.WriteString(fence)
	buf.WriteString("\n")
	buf.WriteString(strings.TrimRight(body, "\n"))
	buf.WriteString("\n")
	return buf.Bytes(), nil
}

// Unmarshal splits front matter into meta and returns the body.
func Unmarshal(data []byte, meta any) (string, error) {
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(s, fence) {
		return "", errors.New("missing front matter")
	}
	rest := s[len(fence):]
	end := strings.Index(rest, "\n"+fence)
	if end < 0 {
		return "", errors.New("unterminated front matter")
	}
	if err := yaml.Unmarshal([]byte(rest[:end+1]), meta); err != nil {
		return "", err
	}
	return strings.TrimPrefix(rest[end+1+len(fence):], "\n"), nil
}

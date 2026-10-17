// Package agents 解析已发布的角色与凭据身份登记，不处理权限。
package agents

import (
	"fmt"
	"github.com/ketchupz1999/agents-home-public/internal/protocol"
	"github.com/ketchupz1999/agents-home-public/internal/store"
	"gopkg.in/yaml.v3"
	"strings"
)

type Role struct {
	ID                   string     `yaml:"id" json:"id"`
	Workspace            string     `yaml:"workspace" json:"workspace"`
	Tags                 []string   `yaml:"tags" json:"tags"`
	Character            string     `yaml:"character" json:"character"`
	Invocation           string     `yaml:"invocation" json:"invocation"`
	WorkspaceDescription string     `yaml:"workspace_description" json:"workspace_description"`
	Repository           Repository `yaml:"repository" json:"repository"`
	Locations            []Location `yaml:"locations" json:"locations"`
	Skills               []string   `yaml:"skills" json:"skills"`
}
type Principal struct {
	ID          string `yaml:"id" json:"id"`
	Environment string `yaml:"environment" json:"environment"`
	Agent       string `yaml:"agent" json:"agent"`
	RoleID      string `yaml:"role_id" json:"role_id"`
	// Aliases are former IDs of this principal (renamed keys or role IDs);
	// old messages that carry them are counted as this principal.
	Aliases []string `yaml:"aliases,omitempty" json:"aliases,omitempty"`
}
type Crop struct {
	X      int `yaml:"x"`
	Y      int `yaml:"y"`
	Width  int `yaml:"width"`
	Height int `yaml:"height"`
}
type Card struct {
	DisplayName string `yaml:"display_name"`
	AvatarText  string `yaml:"avatar_text"`
	AvatarColor string `yaml:"avatar_color"`
	AvatarImage string `yaml:"avatar_image"`
	AvatarCrop  *Crop  `yaml:"avatar_crop"`
	Bio         string `yaml:"bio"`
}
type Registry struct {
	SHA    string          `yaml:"-"`
	Roles  []Role          `yaml:"roles"`
	Agents []Principal     `yaml:"agents"`
	Cards  map[string]Card `yaml:"-"`
}

// RoleFor 返回登记角色；未映射身份保留空值。
func (r *Registry) RoleFor(principal string) string {
	for _, a := range r.Agents {
		if a.ID == principal {
			return a.RoleID
		}
	}
	return ""
}

// Canonical maps a former ID declared in aliases to the current principal;
// any other ID is returned unchanged.
func (r *Registry) Canonical(id string) string {
	for _, a := range r.Agents {
		for _, old := range a.Aliases {
			if old == id {
				return a.ID
			}
		}
	}
	return id
}

// HasRole reports whether id is a currently published role.
func (r *Registry) HasRole(id string) bool {
	for _, role := range r.Roles {
		if role.ID == id {
			return true
		}
	}
	return false
}

// Load 在同一个已发布提交读取名册与名片。
func Load(s *store.Store) (*Registry, error) {
	sha, err := s.PublishedSHA()
	if err != nil {
		return nil, err
	}
	raw, err := s.ReadSnapshot(sha, "agents/registry.yaml")
	if err != nil {
		return nil, err
	}
	if len(raw) > 65536 {
		return nil, fmt.Errorf("registry too large")
	}
	var r Registry
	if err = yaml.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	r.SHA = sha
	r.Cards = map[string]Card{}
	roles := map[string]bool{}
	ids := map[string]bool{}
	for _, role := range r.Roles {
		if !protocol.ValidName(role.ID) || roles[role.ID] {
			return nil, fmt.Errorf("invalid or duplicate role")
		}
		if err := validateWorkspace(role); err != nil {
			return nil, fmt.Errorf("role %s: %w", role.ID, err)
		}
		switch role.Character {
		case "", "muse", "claude", "codex", "chatgpt", "placeholder":
		default:
			return nil, fmt.Errorf("unknown character")
		}
		roles[role.ID] = true
	}
	for _, a := range r.Agents {
		if !protocol.ValidName(a.ID) || ids[a.ID] {
			return nil, fmt.Errorf("invalid or duplicate principal")
		}
		if roles[a.ID] && a.RoleID != a.ID {
			return nil, fmt.Errorf("ambiguous role and principal ID")
		}
		if a.RoleID != "" && !roles[a.RoleID] {
			return nil, fmt.Errorf("unknown role")
		}
		ids[a.ID] = true
	}
	aliases := map[string]bool{}
	for _, a := range r.Agents {
		for _, old := range a.Aliases {
			if !protocol.ValidName(old) || ids[old] || roles[old] || aliases[old] {
				return nil, fmt.Errorf("invalid or ambiguous alias %s", old)
			}
			aliases[old] = true
		}
	}
	for id := range roles {
		ids[id] = true
	}
	for id := range ids {
		b, e := s.ReadSnapshot(sha, "agents/"+id+"/card.md")
		if e != nil {
			if roles[id] {
				return nil, fmt.Errorf("role card %s unavailable", id)
			}
			continue
		}
		if len(b) > 65536 {
			return nil, fmt.Errorf("card too large")
		}
		var c Card
		if strings.HasPrefix(strings.ReplaceAll(string(b), "\r\n", "\n"), "---\n") {
			if _, e = protocol.Unmarshal(b, &c); e != nil {
				return nil, fmt.Errorf("invalid card %s", id)
			}
		}
		r.Cards[id] = c
	}
	return &r, nil
}

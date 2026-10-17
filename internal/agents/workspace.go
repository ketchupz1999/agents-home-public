package agents

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// Repository 声明主工作仓，不承载实际 Git 同步状态。
type Repository struct {
	Status string `yaml:"status" json:"status"`
	URL    string `yaml:"url" json:"url"`
}

// Location 是一个已登记工作地点，与运行实例和在线状态无关。
type Location struct {
	DeviceID   string `yaml:"device_id" json:"device_id"`
	DeviceName string `yaml:"device_name" json:"device_name"`
	Path       string `yaml:"path" json:"path"`
	Hosting    string `yaml:"hosting" json:"hosting"`
}

// WorkLocations 优先返回多地点登记，旧配置只作为未标明设备的路径展示。
func (r Role) WorkLocations() []Location {
	if len(r.Locations) > 0 {
		return r.Locations
	}
	if r.Workspace != "" {
		return []Location{{Path: r.Workspace}}
	}
	return nil
}

// SkillName 只解析本仓库提供的说明，登记不会授予操作权限。
func SkillName(id string) string {
	switch id {
	case "aspace-data":
		return "共享资料"
	case "aspace-mailbox":
		return "Agent 信箱"
	case "aspace-ops":
		return "共享空间运维"
	default:
		return ""
	}
}

func validateWorkspace(r Role) error {
	switch r.Repository.Status {
	case "", "unknown", "none":
		if r.Repository.URL != "" {
			return fmt.Errorf("repository URL requires git status")
		}
	case "git":
		u, err := url.Parse(r.Repository.URL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("repository must be an HTTPS URL without credentials, query or fragment")
		}
	default:
		return fmt.Errorf("invalid repository status")
	}
	if len(r.Locations) > 16 || len(r.Skills) > 3 {
		return fmt.Errorf("too many locations or skills")
	}
	texts := []string{r.WorkspaceDescription, r.Repository.URL}
	for _, l := range r.Locations {
		switch l.Hosting {
		case "", "unknown", "self_managed", "platform_managed":
		default:
			return fmt.Errorf("invalid hosting")
		}
		texts = append(texts, l.DeviceID, l.DeviceName, l.Path)
	}
	for _, s := range texts {
		if len(s) > 1024 || strings.IndexFunc(s, unicode.IsControl) >= 0 {
			return fmt.Errorf("invalid workspace text")
		}
	}
	seen := map[string]bool{}
	for _, id := range r.Skills {
		if SkillName(id) == "" || seen[id] {
			return fmt.Errorf("invalid or duplicate skill")
		}
		seen[id] = true
	}
	return nil
}

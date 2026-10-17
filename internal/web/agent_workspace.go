package web

import (
	"fmt"
	"strings"

	"github.com/ketchupz1999/agents-home-public/internal/agents"
)

func hostingLabel(s string) string {
	switch s {
	case "self_managed":
		return "自主管理"
	case "platform_managed":
		return "平台托管"
	default:
		return "托管方式未知"
	}
}

func repositoryLabel(s string) string {
	switch s {
	case "git":
		return "Git 工作仓"
	case "none":
		return "无独立 Git 仓库"
	default:
		return "Git 状态未登记"
	}
}

func workspaceSummary(r *agents.Role) string {
	if r.WorkspaceDescription != "" {
		return r.WorkspaceDescription
	}
	if len(r.Locations) > 0 {
		return fmt.Sprintf("%d 个工作地点", len(r.Locations))
	}
	return r.Workspace
}

func agentWorkspace(n *teamNode) string {
	var b strings.Builder
	b.WriteString(`<section class="team-activity agent-workspace-panel"><h2>工作空间</h2>`)
	if n.Role == nil {
		return b.String() + `<p class="team-empty">尚未登记工作空间</p></section>`
	}
	r := n.Role
	if r.WorkspaceDescription != "" {
		b.WriteString(`<p class="workspace-description">` + esc(r.WorkspaceDescription) + `</p>`)
	}
	b.WriteString(`<div class="workspace-repository"><span class="team-kicker">主工作仓</span><strong>` + repositoryLabel(r.Repository.Status) + `</strong>`)
	if r.Repository.Status == "git" {
		fmt.Fprintf(&b, `<a href="%s" class="workspace-repo-link">%s ↗</a>`, esc(r.Repository.URL), esc(strings.TrimPrefix(r.Repository.URL, "https://")))
	}
	b.WriteString(`</div><h3>工作地点</h3><div class="workspace-locations">`)
	locations := r.WorkLocations()
	if len(locations) == 0 {
		b.WriteString(`<p class="team-empty">尚未登记工作地点</p>`)
	}
	for _, l := range locations {
		name := l.DeviceName
		if name == "" {
			name = l.DeviceID
		}
		if name == "" {
			name = "设备未登记"
		}
		fmt.Fprintf(&b, `<article class="workspace-location"><div class="workspace-location-title"><strong>%s</strong><span class="team-tag">%s</span></div><dl class="team-kv">%s%s</dl></article>`, esc(name), hostingLabel(l.Hosting), teamKV("机器代号", l.DeviceID), teamKV("目录", l.Path))
	}
	b.WriteString(`</div><h3>Skill 使用说明</h3><div class="workspace-skills">`)
	if len(r.Skills) == 0 {
		b.WriteString(`<p class="team-empty">尚未登记使用说明</p>`)
	}
	for _, id := range r.Skills {
		fmt.Fprintf(&b, `<a class="workspace-skill" href="https://github.com/ketchupz1999/agents-home-public/blob/main/skills/%s/SKILL.md"><span><strong>%s</strong><small>%s</small></span><span aria-hidden="true">↗</span></a>`, esc(id), esc(agents.SkillName(id)), esc(id))
	}
	b.WriteString(`</div><p class="team-hint">使用说明不代表已安装或已获权限。运行记录见“运行”。</p></section>`)
	return b.String()
}

func workspaceMeta(r *agents.Role) [][3]string {
	rows := [][3]string{{"workspace_description", r.WorkspaceDescription, "角色声明"}, {"repository.status", repositoryLabel(r.Repository.Status), "角色声明"}, {"repository.url", r.Repository.URL, "角色声明"}, {"skills", strings.Join(r.Skills, ", "), "可用说明，非安装或权限记录"}}
	for i, l := range r.WorkLocations() {
		prefix := fmt.Sprintf("locations[%d].", i)
		source := "地点登记，非运行观测"
		if len(r.Locations) == 0 {
			source = "旧 workspace 路径，设备未知"
		}
		rows = append(rows, [3]string{prefix + "device_id", l.DeviceID, source}, [3]string{prefix + "device_name", l.DeviceName, source}, [3]string{prefix + "path", l.Path, source}, [3]string{prefix + "hosting", hostingLabel(l.Hosting), source})
	}
	return rows
}

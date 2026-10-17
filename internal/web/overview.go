package web

import (
	_ "embed"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ketchupz1999/agents-home-public/internal/agents"
	"github.com/ketchupz1999/agents-home-public/internal/model"
)

//go:embed overview.css
var overviewCSS string

//go:embed overview.js
var overviewJS string

func (w *Web) overview(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Cache-Control", "no-store")
	if err := w.Store.Refresh(); err != nil {
		http.Error(rw, "资料暂时无法读取，请稍后刷新", 503)
		return
	}
	g, err := model.Load(w.Store)
	if err != nil {
		http.Error(rw, "资料暂时无法读取，请稍后刷新", 503)
		return
	}
	focus, focusErr := w.DB.GetFocus()
	unread, unreadErr := w.DB.ListUnreadGroupMessages(w.owner())
	pending, pendingErr := w.Store.ListBranches()
	goals, items := []*model.Node{}, []*model.Node{}
	for _, n := range g.Nodes {
		switch n.Type {
		case model.TypeGoal:
			goals = append(goals, n)
		case model.TypeItem:
			items = append(items, n)
		}
	}
	byStatus(goals)
	var b strings.Builder
	b.WriteString(`<style>` + overviewCSS + `</style><div class="overview"><header class="space-hero"><div><p class="space-kicker">个人多 Agent 共享空间</p><h1>我的空间</h1>`)
	fmt.Fprintf(&b, `<p class="space-meta">%d 个目标 <span>·</span> %d 个事项</p></div>`, len(goals), len(items))
	b.WriteString(w.overviewTeam())
	b.WriteString(`</header>`)
	b.WriteString(w.actionBoard(g))
	b.WriteString(`<div class="working-grid"><section><div class="section-head"><h2>当前关注</h2>`)
	if focusErr == nil {
		b.WriteString(`<a class="text-button" href="#focus-editor" data-open-focus>调整置顶 ↗</a>`)
	}
	b.WriteString(`</div>`)
	if focusErr != nil {
		b.WriteString(`<p class="overview-error" role="alert">关注暂时无法读取，请稍后刷新。</p>`)
	} else if len(focus.Items) == 0 {
		b.WriteString(`<div class="focus-empty"><span class="empty-spark" aria-hidden="true">✧</span><h3>把想推进的事放在这里</h3><a href="#focus-editor" class="text-button" data-open-focus>选择关注事项 ↗</a></div>`)
	} else {
		for i, id := range focus.Items {
			n := g.Get("item:" + id)
			if n == nil {
				fmt.Fprintf(&b, `<div class="focus-missing">%s 已不存在。可调整置顶移除。</div>`, esc(id))
				continue
			}
			if i == 0 {
				fmt.Fprintf(&b, `<article class="focus-primary"><div class="focus-top"><span>01 / 当前关注</span><span class="focus-status">%s</span></div><h3>%s</h3><p class="focus-subtitle">%s</p><div class="focus-footer"><span>%s</span><a class="continue" href="/d/item/%s">继续推进 <span class="round-arrow" aria-hidden="true">→</span></a></div></article>`, esc(overviewStatus(n.Status)), esc(n.Title), esc(overviewParents(g, n)), esc(strings.Join(n.Domains, " · ")), url.PathEscape(n.ID))
			} else {
				fmt.Fprintf(&b, `<a class="focus-secondary" href="/d/item/%s"><span class="secondary-number">02</span><span class="secondary-body"><strong>%s</strong><small>%s</small></span><span class="secondary-arrow" aria-hidden="true">↗</span></a>`, url.PathEscape(n.ID), esc(n.Title), esc(overviewParents(g, n)))
			}
		}
	}
	b.WriteString(`</section><aside><div class="section-head"><h2>待我处理`)
	if pendingErr == nil {
		fmt.Fprintf(&b, `<span class="pending-count">%d</span>`, countPending(w, pending))
	}
	b.WriteString(`</h2></div>`)
	if pendingErr != nil {
		b.WriteString(`<p class="overview-error">提案暂时无法读取。<a href="/d/branches">打开待确认 ↗</a></p>`)
	} else {
		shown := false
		for _, br := range pending {
			if w.DB.BranchStatus(br.Name) == "rejected" {
				continue
			}
			fmt.Fprintf(&b, `<article class="overview-proposal"><div class="proposal-by"><span>%s</span><time>%s</time></div><h3>%s</h3><p class="proposal-files">%d 个文件变更</p><a class="proposal-open" href="/d/branches">查看提案 <span aria-hidden="true">↗</span></a></article>`, esc(w.identity(br.Agent).Name), esc(br.Date), esc(br.Subject), len(br.Files))
			shown = true
			break
		}
		if !shown {
			b.WriteString(`<div class="pending-empty"><span aria-hidden="true">✓</span><p>暂时没有待确认的提案</p><a href="/d/branches">查看提案 ↗</a></div>`)
		}
	}
	if unreadErr != nil {
		b.WriteString(`<p class="overview-error">未读消息暂时无法读取。<a href="/d/groups">打开群组 ↗</a></p>`)
	} else {
		fmt.Fprintf(&b, `<a class="inbox-strip" href="/d/groups"><span aria-hidden="true">✉</span><span>未读消息</span><strong>%d</strong><span aria-hidden="true">↗</span></a>`, len(unread))
	}
	b.WriteString(`</aside></div><section class="goals-section"><div class="section-head"><h2>长期目标`)
	fmt.Fprintf(&b, `<span class="section-count">%d</span></h2><div class="view-switch" hidden aria-label="目标视图"><button type="button" data-view="goal-list" aria-pressed="true" aria-controls="goal-list">列表</button><button type="button" data-view="goal-map" aria-pressed="false" aria-controls="goal-map">关系图</button></div></div><div id="goal-list" class="overview-goals">`, len(goals))
	for _, n := range goals {
		fmt.Fprintf(&b, `<a class="overview-goal" href="/d/item/%s"><span class="goal-symbol">%s</span><span class="goal-name">%s</span><span class="goal-kind">%s</span><span aria-hidden="true">↗</span></a>`, url.PathEscape("goal:"+n.ID), nodeIcon(n.Type), esc(n.Title), esc(overviewKind(n.Kind)))
	}
	b.WriteString(`</div>` + goalMap(g, goals) + `</section><div class="secondary-sections"><details class="other-section"><summary>其他事项 <span class="section-count">`)
	selected := map[string]bool{}
	if focusErr == nil {
		for _, id := range focus.Items {
			selected[id] = true
		}
	}
	remaining := 0
	for _, n := range items {
		if !selected[n.ID] {
			remaining++
		}
	}
	fmt.Fprintf(&b, `%d</span><span class="chevron" aria-hidden="true">›</span></summary>`, remaining)
	for _, n := range items {
		if !selected[n.ID] {
			fmt.Fprintf(&b, `<a class="other-item" href="/d/item/%s"><span>%s</span><small>%s</small></a>`, url.PathEscape(n.ID), esc(n.Title), esc(overviewStatus(n.Status)))
		}
	}
	b.WriteString(`</details><details class="other-section"><summary>人物 <span class="section-count">`)
	people := w.people(g)
	fmt.Fprintf(&b, `%d</span><span class="chevron" aria-hidden="true">›</span></summary>`, len(people))
	for _, p := range people {
		fmt.Fprintf(&b, `<a class="other-item" href="/d/item/%s"><span>%s</span><small>%s</small></a>`, url.PathEscape("person:"+p.ID), esc(p.Title), esc(personSub(p)))
	}
	b.WriteString(`</details>`)
	looseLines := []*model.Node{}
	for _, n := range g.Nodes {
		if n.Type != model.TypeLine {
			continue
		}
		ancestors, _ := g.Ancestors(n)
		hasGoal := false
		for _, a := range ancestors {
			if a.Type == model.TypeGoal {
				hasGoal = true
				break
			}
		}
		if !hasGoal {
			looseLines = append(looseLines, n)
		}
	}
	if len(looseLines) > 0 {
		fmt.Fprintf(&b, `<details class="other-section"><summary>独立推进线 <span class="section-count">%d</span><span class="chevron" aria-hidden="true">›</span></summary>`, len(looseLines))
		for _, n := range looseLines {
			fmt.Fprintf(&b, `<a class="other-item" href="/d/item/%s"><span>%s</span><small>%s</small></a>`, url.PathEscape("line:"+n.ID), esc(n.Title), esc(overviewStatus(n.Status)))
		}
		b.WriteString(`</details>`)
	}
	b.WriteString(`</div>`)
	if focusErr == nil {
		b.WriteString(`<details id="focus-editor" class="focus-editor"><summary>调整关注事项</summary><div><h2>想先推进哪两件事？</h2>` + w.focusForm(g, focus.Items, focus.Revision) + `</div></details>`)
	}
	fmt.Fprintf(&b, `<footer class="space-footer"><span>个人多 Agent 共享空间</span><span>数据仓 %s</span></footer></div><script>%s</script>`, esc(shortSHA(w.Store)), overviewJS)
	page(rw, "我的空间", "/d/", b.String())
}

func overviewStatus(status string) string {
	switch status {
	case "active":
		return "进行中"
	case "done":
		return "已完成"
	case "paused":
		return "暂缓"
	default:
		return status
	}
}
func overviewParents(g *model.Graph, n *model.Node) string {
	ancestors, _ := g.Ancestors(n)
	names := []string{}
	for _, a := range ancestors {
		if a.Type == model.TypeGoal {
			names = append(names, a.Title)
		}
	}
	if len(names) == 0 {
		return "独立事项"
	}
	return strings.Join(names, " · ")
}
func (w *Web) overviewTeam() string {
	var b strings.Builder
	b.WriteString(`<div class="overview-team"><div class="team-top"><a href="/d/agents">我的小队 ↗</a><button id="overview-motion" type="button" hidden aria-pressed="true">暂停动效</button></div><div class="team-members">`)
	reg, err := agents.Load(w.Store)
	if err != nil {
		return b.String() + `<p class="muted">小队暂时无法读取</p></div></div>`
	}
	list := []agentIdentity{}
	for _, role := range reg.Roles {
		if role.ID != "owner" {
			list = append(list, w.identityAt(reg, role.ID, role.ID))
		}
	}
	for _, p := range reg.Agents {
		if p.ID != "owner" && reg.RoleFor(p.ID) == "" {
			list = append(list, w.identityAt(reg, p.ID, ""))
		}
	}
	for _, v := range list {
		href := "/d/agents/" + url.PathEscape(v.ID)
		if bundle, ok := teamBundle(v); ok {
			fmt.Fprintf(&b, `<a class="teammate" href="%s" aria-label="查看 %s"><span class="overview-actor" data-manifest="%s" data-character="%s"><span class="actor-fallback">%s</span><canvas width="160" height="160" hidden aria-hidden="true"></canvas></span><span>%s</span></a>`, esc(href), esc(v.Name), esc(bundle.Manifest), esc(bundle.ID), avatar(v), esc(v.Name))
		} else {
			fmt.Fprintf(&b, `<a class="teammate" href="%s"><span class="overview-actor">%s</span><span>%s</span></a>`, esc(href), avatar(v), esc(v.Name))
		}
	}
	b.WriteString(`</div></div>`)
	return b.String()
}

func overviewKind(kind string) string {
	return map[string]string{"outcome": "结果型", "explore": "探索型", "maintain": "维护型", "gated": "条件触发型"}[kind]
}

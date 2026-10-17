// Package web serves a small mobile-first dashboard for the shared space:
// overview, mailbox, pending branches and item pages. Plain HTML, no build step.
package web

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ketchupz1999/agents-home-public/internal/db"
	"github.com/ketchupz1999/agents-home-public/internal/model"
	"github.com/ketchupz1999/agents-home-public/internal/protocol"
	"github.com/ketchupz1999/agents-home-public/internal/store"
)

type Web struct {
	Store *store.Store
	DB    *db.DB
	// SecretKey seeds form CSRF tokens; session bearers are random and revocable.
	SecretKey []byte
	// Auth checks a submitted secret and returns the owner's agent name,
	// so the dashboard never needs the key in plaintext.
	Auth              func(secret string) (string, bool)
	Now               func() time.Time
	AccessForIdentity func(string) (IdentityAccess, bool)
	// CreateGroup opens a group as the named key holder; nil disables the form.
	CreateGroup func(creator string, in protocol.GroupInput) (*protocol.Group, string, error)
	// KeyNames lists identities holding a key, offered as group members.
	KeyNames func() []string
	// UpdateGroupMembers changes a group's member list; nil hides the form.
	UpdateGroupMembers func(caller, groupID string, in protocol.GroupMemberInput) (*protocol.Group, []string, []string, int, error)
	sessionMu          sync.Mutex
	sessions           map[string]time.Time
}

const cookieName = "aspace_session"

func (w *Web) Handler() http.Handler {
	mux := http.NewServeMux()
	write := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, w.auth(h)) }
	mux.HandleFunc("GET /d/", w.auth(w.overview))
	write("POST /d/focus", w.saveFocus)
	write("POST /d/actions/{id}/{operation}", w.saveAction)
	mux.HandleFunc("GET /d/agents", w.auth(w.agents))
	mux.HandleFunc("GET /d/agents/{role}", w.auth(w.agentDetail))
	mux.HandleFunc("GET /d/avatar/{id}", w.auth(w.avatarImage))
	mux.HandleFunc("GET /d/characters/{file}", w.auth(characterImage))
	mux.HandleFunc("GET /d/groups", w.auth(w.groups))
	mux.HandleFunc("GET /d/groups/new", w.auth(w.newGroup))
	write("POST /d/groups", w.postGroup)
	mux.HandleFunc("GET /d/groups/{id}", w.auth(w.groupDetail))
	write("POST /d/groups/{id}/ack", w.ackGroup)
	write("POST /d/groups/{id}/members", w.postGroupMembers)
	mux.HandleFunc("GET /d/branches", w.auth(w.branches))
	mux.HandleFunc("GET /d/item/{id}", w.auth(w.item))
	mux.HandleFunc("GET /d/file", w.auth(w.file))
	write("POST /d/ack/{id}", w.ack)
	write("POST /d/branch/{name...}", w.branchAction)
	mux.HandleFunc("GET /d/login", w.loginForm)
	mux.HandleFunc("POST /d/login", w.login)
	mux.HandleFunc("GET /d/logout", w.logout)
	return w.protect(mux)
}

// ---- session ----

func (w *Web) owner() string { return "owner" }

func (w *Web) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(cookieName); err == nil && w.validSession(c.Value) {
			next(rw, r)
			return
		}
		http.Redirect(rw, r, "/d/login", http.StatusSeeOther)
	}
}

func (w *Web) loginForm(rw http.ResponseWriter, r *http.Request) {
	page(rw, "登录", "", `
<div class="login"><p class="eyebrow">你的 Agent 协作空间</p><h1>欢迎回来</h1>
<p class="subtitle">目标、消息与每一次需要你的决定。</p>
<form method="post" action="/d/login" class="card">
  <label for="key">Owner key</label>
  <input id="key" type="password" name="key" autocomplete="current-password" autofocus>
  <button type="submit">进入共享空间</button>
  <p class="muted">使用你的 owner key 登录。请妥善保管这份凭据。</p>
</form></div>`)
}

func (w *Web) login(rw http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(rw, r.Body, 8192)
	if err := r.ParseForm(); err != nil {
		http.Error(rw, "invalid login form", 400)
		return
	}
	if _, ok := w.Auth(r.FormValue("key")); ok {
		// Secure only over real TLS: behind a plain-HTTP test server a secure
		// cookie would be dropped by the client.
		http.SetCookie(rw, &http.Cookie{Name: cookieName, Value: w.token(), Path: "/d",
			HttpOnly: true, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
			SameSite: http.SameSiteLaxMode, MaxAge: int(sessionLifetime.Seconds())})
		http.Redirect(rw, r, "/d/", http.StatusSeeOther)
		return
	}
	rw.WriteHeader(http.StatusUnauthorized)
	page(rw, "登录", "", `<h1>共享空间</h1><div class="card error">key 不对。</div><p><a href="/d/login">重试</a></p>`)
}

func (w *Web) logout(rw http.ResponseWriter, r *http.Request) {
	w.revokeSession(r)
	http.SetCookie(rw, &http.Cookie{Name: cookieName, Value: "", Path: "/d", MaxAge: -1})
	http.Redirect(rw, r, "/d/login", http.StatusSeeOther)
}

// ---- pages ----

func (w *Web) ack(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := w.DB.MarkRead(w.owner(), id); err != nil {
		page(rw, "信箱", "/d/groups", "<div class='card error'>标记失败</div>")
		return
	}
	http.Redirect(rw, r, "/d/groups", http.StatusSeeOther)
}

func (w *Web) branches(rw http.ResponseWriter, r *http.Request) {
	if err := w.Store.FetchBranches(); err != nil {
		page(rw, "待确认", "/d/branches", "<div class='card error'>拉取分支失败</div>")
		return
	}
	branches, err := w.Store.ListBranches()
	if err != nil {
		page(rw, "待确认", "/d/branches", "<div class='card error'>列分支失败</div>")
		return
	}
	var b strings.Builder
	b.WriteString(`<header class="page-heading"><div><p class="eyebrow">由你决定下一步</p><h1>待确认</h1></div></header><p class="subtitle">查看 Agent 提交的改动，再决定是否合并。</p>`)
	identities := w.identities(nil)
	n := 0
	for _, br := range branches {
		if w.DB.BranchStatus(br.Name) == "rejected" {
			continue
		}
		n++
		diff, _ := w.Store.BranchDiff(br.Name)
		fmt.Fprintf(&b, `<article class="card proposal">
<div class="message-head"><b>%s</b><time class="meta">%s</time></div><div class="meta branch-name">%s</div>
<h2 class="task">%s</h2>
<div class="files">%s</div>
<details class="message-body"><summary>查看改动 diff</summary><pre class="diff">%s</pre></details>
<div class="actions">
  <form method="post" action="/d/branch/%s/apply"><button type="submit">通过并合并</button></form>
  <form method="post" action="/d/branch/%s/reject"><button type="submit" class="ghost danger">拒绝</button></form>
</div></article>`, identityBadge(branchIdentity(identities, w, br.Agent)), esc(br.Date), esc(br.Name), esc(br.Subject),
			esc(strings.Join(br.Files, " ")), esc(truncate(diff, 4000)), esc(br.Name), esc(br.Name))
	}
	if n == 0 {
		b.WriteString(`<div class="card empty"><h2>所有改动都已处理</h2><p class="muted">新的提交会出现在这里，等你确认。</p></div>`)
	}
	page(rw, "待确认", "/d/branches", b.String())
}

func (w *Web) branchAction(rw http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	action := ""
	switch {
	case strings.HasSuffix(name, "/apply"):
		name, action = strings.TrimSuffix(name, "/apply"), "apply"
	case strings.HasSuffix(name, "/reject"):
		name, action = strings.TrimSuffix(name, "/reject"), "reject"
	default:
		http.NotFound(rw, r)
		return
	}
	var err error
	status := ""
	if action == "apply" {
		err = w.Store.ApplyBranch(name, w.owner())
		status = "applied"
	} else {
		err = w.Store.DeleteBranch(name)
		status = "rejected"
	}
	rec := db.BranchDecision{Name: name, CreatedAt: w.Now().UTC().Format(time.RFC3339),
		Status: status, DecidedAt: w.Now().UTC().Format(time.RFC3339), DecidedBy: w.owner()}
	if agent, topic, ok := store.ParseBranchName(name); ok {
		rec.Agent, rec.Topic = agent, topic
	}
	if err != nil {
		rec.Status = "conflict"
		_ = w.DB.RecordBranch(rec)
		w.DB.Audit(w.owner(), "branch.conflict", name)
		page(rw, "待确认", "/d/branches", fmt.Sprintf(`<h1>合并失败</h1>
<div class="card error">和 main 有冲突，需要 %s 重新 rebase 后再提。</div>
<p><a href="/d/branches">返回</a></p>`, esc(rec.Agent)))
		return
	}
	_ = w.DB.RecordBranch(rec)
	w.DB.Audit(w.owner(), "branch."+status, name)
	http.Redirect(rw, r, "/d/branches", http.StatusSeeOther)
}

func (w *Web) item(rw http.ResponseWriter, r *http.Request) {
	g, err := model.Load(w.Store)
	id := r.PathValue("id")
	n := g.Get(id)
	if err != nil || n == nil {
		page(rw, "事项", "/d/", "<div class='card error'>找不到</div>")
		return
	}
	ancestors, _ := g.Ancestors(n)
	var b strings.Builder
	fmt.Fprintf(&b, `<a class="back" href="/d/">← 返回总览</a><header class="page-heading"><h1>%s %s</h1></header><div class="item-tags">%s</div>`, nodeIcon(n.Type), esc(n.Title), nodeTags(n))
	if len(ancestors) > 0 {
		b.WriteString(`<div class="meta">上级：`)
		for i, a := range ancestors {
			if i > 0 {
				b.WriteString(" · ")
			}
			fmt.Fprintf(&b, `<a href="/d/item/%s">%s</a>`, esc(a.ID), esc(a.Title))
		}
		b.WriteString(`</div>`)
	}
	body, err := w.Store.Read(n.Path)
	if err == nil {
		fmt.Fprintf(&b, `<div class="card"><pre class="doc">%s</pre></div>`, esc(string(body)))
	}
	files, _ := w.Store.List("items/" + n.ID)
	if len(files) > 0 {
		b.WriteString(`<h2>文件</h2><ul class="list">`)
		for _, f := range files {
			if strings.HasSuffix(f, "CONTEXT.md") {
				continue
			}
			fmt.Fprintf(&b, `<li><a href="/d/file?path=%s">%s</a></li>`, esc(url.QueryEscape(f)), esc(f))
		}
		b.WriteString(`</ul>`)
	}
	groups, groupErr := w.DB.ListGroups("", n.ID, "all")
	if groupErr != nil {
		b.WriteString(`<p class="muted">暂时无法读取群组。</p>`)
	} else if len(groups) > 0 {
		fmt.Fprintf(&b, `<h2>群组（%d 个）</h2><ul class="list">`, len(groups))
		for _, g := range groups {
			muted := ""
			if g.Status == "closed" {
				muted = ` · 已关闭`
			}
			fmt.Fprintf(&b, `<li><a href="/d/groups/%s">%s</a><span class="muted">%s</span></li>`,
				esc(url.PathEscape(g.ID)), esc(g.Topic), esc(muted))
		}
		b.WriteString(`</ul>`)
	}

	page(rw, n.Title, "/d/", b.String())
}

func (w *Web) file(rw http.ResponseWriter, r *http.Request) {
	rel, err := store.Clean(r.URL.Query().Get("path"))
	if err != nil {
		page(rw, "文件", "/d/", "<div class='card error'>路径不对</div>")
		return
	}
	body, err := w.Store.Read(rel)
	if err != nil {
		page(rw, "文件", "/d/", "<div class='card error'>读不到</div>")
		return
	}
	page(rw, rel, "/d/", fmt.Sprintf(`<a class="back" href="/d/">← 返回总览</a><h1 class="file-title">%s</h1><div class="card"><pre class="doc">%s</pre></div>`,
		esc(rel), esc(truncate(string(body), 60000))))
}

// ---- helpers ----

func countPending(w *Web, branches []store.Branch) int {
	n := 0
	for _, b := range branches {
		if w.DB.BranchStatus(b.Name) != "rejected" {
			n++
		}
	}
	return n
}

func byStatus(ns []*model.Node) {
	order := map[string]int{"active": 0, "paused": 1, "done": 2, "dropped": 3}
	sort.SliceStable(ns, func(i, j int) bool { return order[ns[i].Status] < order[ns[j].Status] })
}

func looseNodes(g *model.Graph) []*model.Node {
	placed := map[*model.Node]bool{}
	for _, n := range g.Nodes {
		for _, c := range g.Children(n) {
			placed[c] = true
		}
	}
	var out []*model.Node
	for _, n := range g.Nodes {
		if n.Type == model.TypeGoal || n.Type == model.TypePerson || placed[n] {
			continue
		}
		if len(n.Serves) > 0 {
			continue
		}
		out = append(out, n)
	}
	byStatus(out)
	return out
}

// Icons describe node types without exposing storage enums in the interface.
func nodeIcon(typ string) string {
	paths := map[string]string{
		model.TypeGoal:   `<circle cx="12" cy="12" r="8"/><circle cx="12" cy="12" r="3"/>`,
		model.TypeLine:   `<path d="M6 4v10a4 4 0 0 0 4 4h8M14 14l4 4-4 4"/>`,
		model.TypeItem:   `<rect x="5" y="5" width="14" height="14" rx="3"/>`,
		model.TypePerson: `<circle cx="12" cy="8" r="3"/><path d="M5 21v-3a7 7 0 0 1 14 0v3"/>`,
	}
	labels := map[string]string{model.TypeGoal: "目标", model.TypeLine: "线", model.TypeItem: "事项", model.TypePerson: "人物"}
	return fmt.Sprintf(`<svg class="node-icon" role="img" aria-label="%s" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">%s</svg>`, esc(labels[typ]), paths[typ])
}

func nodeTags(n *model.Node) string {
	var b strings.Builder
	kinds := map[string]string{"outcome": "结果型", "explore": "探索型", "maintain": "维护型", "gated": "条件触发型"}
	statuses := map[string]string{"paused": "暂停", "done": "已完成", "dropped": "已放弃"}
	if label := kinds[n.Kind]; label != "" {
		fmt.Fprintf(&b, `<span class="tag">%s</span>`, esc(label))
	}
	if n.Status != "" && n.Status != "active" {
		label := statuses[n.Status]
		if label == "" {
			label = n.Status
		}
		fmt.Fprintf(&b, `<span class="tag status">%s</span>`, esc(label))
	}
	return b.String()
}

func messageKind(kind string) string {
	if label := (map[string]string{"ask": "请求", "reply": "回复", "note": "记录"})[kind]; label != "" {
		return label
	}
	return kind
}

func tree(g *model.Graph, goals []*model.Node) string {
	var b strings.Builder
	for _, n := range goals {
		fmt.Fprintf(&b, `<article class="goal"><div class="goal-head"><a class="node-link" href="/d/item/%s">%s<span>%s</span></a><div class="tags">%s</div></div>`, esc(n.ID), nodeIcon(n.Type), esc(n.Title), nodeTags(n))
		kids := g.Children(n)
		byStatus(kids)
		count := 0
		for _, c := range kids {
			if c.Type != model.TypePerson {
				count++
			}
		}
		if count > 0 || len(n.GatedBy) > 0 {
			fmt.Fprintf(&b, `<details class="goal-details"><summary>关联进展 <span>%d</span></summary>`, count)
			if len(n.GatedBy) > 0 {
				var titles []string
				for _, id := range n.GatedBy {
					if target := g.Get(id); target != nil {
						titles = append(titles, target.Title)
					} else {
						titles = append(titles, id)
					}
				}
				fmt.Fprintf(&b, `<p class="meta gate">等待前提：%s</p>`, esc(strings.Join(titles, "、")))
			}
			b.WriteString(`<ul class="kids">`)
			for _, c := range kids {
				if c.Type == model.TypePerson {
					continue
				}
				fmt.Fprintf(&b, `<li><a class="node-link" href="/d/item/%s">%s<span>%s</span></a>%s`, esc(c.ID), nodeIcon(c.Type), esc(c.Title), nodeTags(c))
				if len(c.Serves) > 1 {
					fmt.Fprintf(&b, `<span class="shared" title="%s">共享 · %s</span>`, esc("关联："+strings.Join(c.Serves, "、")), esc(c.ID))
				}
				// Show items under a line, without recursively following a potentially cyclic graph.
				if c.Type == model.TypeLine {
					b.WriteString(`<ul class="line-items">`)
					for _, item := range g.Children(c) {
						if item.Type != model.TypeItem {
							continue
						}
						fmt.Fprintf(&b, `<li><a class="node-link" href="/d/item/%s">%s<span>%s</span></a>%s</li>`, esc(item.ID), nodeIcon(item.Type), esc(item.Title), nodeTags(item))
					}
					b.WriteString(`</ul>`)
				}
				b.WriteString(`</li>`)
			}
			b.WriteString(`</ul></details>`)
		} else {
			b.WriteString(`<p class="goal-hint">尚未关联事项</p>`)
		}
		b.WriteString(`</article>`)
	}
	return b.String()
}

// goalMap renders each reachable entity once, so shared lines have one identity.
// Positions follow the model's real links; the browser only draws their geometry.
func goalMap(g *model.Graph, goals []*model.Node) string {
	type placedNode struct {
		node     *model.Node
		key, top int
	}
	type edge struct {
		from, to     int
		prerequisite bool
	}
	nodes := []*model.Node{}
	keys := map[*model.Node]int{}
	add := func(n *model.Node) {
		if _, exists := keys[n]; exists {
			return
		}
		keys[n] = len(nodes)
		nodes = append(nodes, n)
	}
	resolveParent := func(id string) *model.Node {
		if n := g.Get("line:" + id); n != nil {
			return n
		}
		return g.Get("goal:" + id)
	}
	for _, goal := range goals {
		add(goal)
	}
	for i := 0; i < len(nodes); i++ {
		// Include co-parents and prerequisites, even when they have no goal of their own.
		references := append(append([]string{}, nodes[i].Serves...), nodes[i].GatedBy...)
		for _, id := range references {
			if parent := resolveParent(id); parent != nil {
				add(parent)
			}
		}
		for _, child := range g.Children(nodes[i]) {
			if child.Type == model.TypeGoal || child.Type == model.TypeLine || child.Type == model.TypeItem {
				add(child)
			}
		}
	}
	var edges []edge
	defaultGoal := 0
	foundDefault := false
	for _, n := range nodes {
		for _, child := range g.Children(n) {
			if to, ok := keys[child]; ok {
				edges = append(edges, edge{keys[n], to, false})
				if !foundDefault && n.Type == model.TypeGoal {
					defaultGoal, foundDefault = keys[n], true
				}
			}
		}
		for _, id := range n.GatedBy {
			parent := resolveParent(id)
			if from, ok := keys[parent]; ok {
				edges = append(edges, edge{from, keys[n], true})
			}
		}
	}
	goalTop := map[*model.Node]int{}
	for i, goal := range goals {
		goalTop[goal] = i * 92
	}
	columns := map[string][]placedNode{}
	for _, n := range nodes {
		top := goalTop[n]
		if n.Type != model.TypeGoal {
			ancestors, _ := g.Ancestors(n)
			sum, count := 0, 0
			for _, ancestor := range ancestors {
				if y, ok := goalTop[ancestor]; ok {
					sum += y
					count++
				}
			}
			if count > 0 {
				top = sum / count
			}
		}
		columns[n.Type] = append(columns[n.Type], placedNode{n, keys[n], top})
	}
	height := 0
	for typ, column := range columns {
		sort.SliceStable(column, func(i, j int) bool { return column[i].top < column[j].top })
		next := 0
		for i := range column {
			if column[i].top < next {
				column[i].top = next
			}
			next = column[i].top + 92
		}
		if next > height {
			height = next
		}
		columns[typ] = column
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<section id="goal-map" class="goal-map" hidden aria-label="目标关系图" data-default-goal="%d"><div class="map-heading"><div><p class="eyebrow">目标 · 路径 · 行动</p><h2>每一步，通向哪里</h2><p class="map-description">%d 个目标，%d 条推进线，%d 个关联事项。</p></div><div class="map-filter"><label for="map-focus">聚焦目标</label><select id="map-focus"><option value="all">全部关系</option>`, defaultGoal, len(goals), len(columns[model.TypeLine]), len(columns[model.TypeItem]))
	for _, goal := range goals {
		fmt.Fprintf(&b, `<option value="%d">%s</option>`, keys[goal], esc(goal.Title))
	}
	b.WriteString(`</select></div></div><div class="map-viewport" tabindex="0" role="region" aria-label="关系画布"><div class="map-plane" style="--map-height:`)
	fmt.Fprintf(&b, `%dpx"><svg class="map-edges" aria-hidden="true">`, height)
	b.WriteString(`<defs><marker id="map-prerequisite-arrow" markerWidth="6" markerHeight="6" refX="5" refY="3" orient="auto"><polygon points="0,0 6,3 0,6" fill="var(--map-gate)"/></marker></defs>`)
	for _, e := range edges {
		kind := "serves"
		if e.prerequisite {
			kind = "prerequisite"
		}
		fmt.Fprintf(&b, `<path data-from="%d" data-to="%d" data-kind="%s"/>`, e.from, e.to, esc(kind))
	}
	b.WriteString(`</svg>`)
	for i, typ := range []string{model.TypeGoal, model.TypeLine, model.TypeItem} {
		label := map[string]string{model.TypeGoal: "目标", model.TypeLine: "推进线", model.TypeItem: "具体事项"}[typ]
		fmt.Fprintf(&b, `<div class="map-column" data-column="%s"><h3><span>0%d</span>%s</h3><div class="map-nodes">`, esc(typ), i+1, esc(label))
		for _, placed := range columns[typ] {
			n := placed.node
			accessible := n.Title
			for _, relation := range edges {
				if relation.to != placed.key {
					continue
				}
				if relation.prerequisite {
					accessible += "；前提："
				} else {
					accessible += "；归属于："
				}
				accessible += nodes[relation.from].Title
			}
			fmt.Fprintf(&b, `<a class="map-node map-%s" data-node="%d" style="--node-top:%dpx" href="/d/item/%s" aria-label="%s"><span class="map-node-title">%s<span>%s</span><span class="map-open" aria-hidden="true">↗</span></span><span class="map-node-meta">%s`, esc(typ), placed.key, placed.top, esc(url.PathEscape(n.Type+":"+n.ID)), esc(accessible), nodeIcon(n.Type), esc(n.Title), nodeTags(n))
			switch n.Type {
			case model.TypeGoal:
				if len(n.GatedBy) > 0 {
					b.WriteString(`<span class="map-wait">有前提</span>`)
				}
			case model.TypeLine:
				if len(n.Serves) > 1 {
					fmt.Fprintf(&b, `<span>共享路径 · 连接 %d 个上级</span>`, len(n.Serves))
				} else {
					b.WriteString(`<span>持续推进</span>`)
				}
			case model.TypeItem:
				if n.Status == "active" || n.Status == "" {
					b.WriteString(`<span>进行中</span>`)
				}
			}
			b.WriteString(`</span></a>`)
		}
		b.WriteString(`</div></div>`)
	}
	b.WriteString(`</div></div><div class="map-footer"><div class="map-legend"><span><i></i>归属关系</span><span><i class="dashed"></i>前提 → 目标</span></div><span class="map-help" aria-live="polite">点击节点查看详情</span></div></section>`)
	return b.String()
}

func (w *Web) people(g *model.Graph) []*model.Node {
	var out []*model.Node
	for _, n := range g.Nodes {
		if n.Type == model.TypePerson {
			out = append(out, n)
		}
	}
	return out
}

func personSub(n *model.Node) string {
	var parts []string
	for _, r := range n.Relations {
		label := map[string]string{"parent": "父母", "me": "本人", "sibling": "手足", "partner": "伴侣", "friend": "朋友", "colleague": "同事"}[r.Type]
		if label == "" {
			label = r.Type
		}
		parts = append(parts, label)
	}
	if n.Kind == "group" {
		parts = append(parts, "群组")
	}
	if len(parts) == 0 {
		return n.ID
	}
	return strings.Join(parts, " · ")
}

func shortSHA(s *store.Store) string {
	lines, err := s.Log(1)
	if err != nil || len(lines) == 0 {
		return ""
	}
	return strings.Fields(lines[0])[0]
}

func esc(s string) string { return html.EscapeString(s) }

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// page renders one page with the shared shell. nav marks the active tab.
func page(rw http.ResponseWriter, title, nav, body string) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	tab := func(href, label string) string {
		cls := ""
		if nav == href {
			cls = ` class="on" aria-current="page"`
		}
		return fmt.Sprintf(`<a href="%s"%s>%s</a>`, esc(href), cls, esc(label))
	}
	// Written with WriteString so CSS percent signs are literal, not verbs.
	navigation := ""
	if nav != "" {
		navigation = `<nav aria-label="主导航">` + tab("/d/", "总览") + tab("/d/groups", "群组") + tab("/d/branches", "待确认") + tab("/d/agents", "我的小队") + `</nav><a class="logout" href="/d/logout">退出</a>`
	}
	out := strings.NewReplacer(
		"{{TITLE}}", esc(title),
		"{{NAV}}", navigation,
		"{{BODY}}", body,
		"{{THREAD_CSS}}", threadCSS,
	).Replace(shell)
	io.WriteString(rw, out)
}

// shell is the page frame; {{TITLE}}, {{NAV}} and {{BODY}} are substituted.
const shell = `<!doctype html><html lang="zh"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark"><title>{{TITLE}} · 共享空间</title>
<style>
:root{--bg:#f6f7f5;--fg:#242b28;--card:#fff;--line:#dfe4df;--muted:#69736d;--accent:#346a54;--soft:#edf3ef;--danger:#b43f3f;--code:#f4f6f3}
@media(prefers-color-scheme:dark){:root{--bg:#171b19;--fg:#e4e9e5;--card:#202622;--line:#37423b;--muted:#a5b2a9;--accent:#9ac9ac;--soft:#2b3b31;--danger:#ffaaa5;--code:#191f1b}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font:14px/1.6 -apple-system,BlinkMacSystemFont,"PingFang SC","Hiragino Sans GB",sans-serif;-webkit-font-smoothing:antialiased}
a{color:inherit;text-decoration:none}button,a,summary{-webkit-tap-highlight-color:transparent}a,button,summary{touch-action:manipulation}a:hover{color:var(--accent)}a:focus-visible,button:focus-visible,summary:focus-visible,input:focus-visible{outline:2px solid var(--accent);outline-offset:4px}
.topbar{border-bottom:1px solid var(--line);background:var(--card)}.topbar-inner{max-width:1056px;margin:auto;min-height:76px;padding:0 28px;display:flex;align-items:center;gap:36px}.brand{display:flex;align-items:center;gap:10px;font-size:15px;font-weight:650;white-space:nowrap}.brand-mark{width:25px;height:25px;border:1.5px solid var(--accent);border-radius:7px;display:grid;place-items:center;color:var(--accent);font-size:17px}nav{display:flex;gap:4px}nav a{padding:9px 20px;min-height:44px;border-radius:7px;color:var(--muted);font-weight:550}nav a.on{color:var(--accent);background:var(--soft)}.logout{margin-left:auto;color:var(--muted);padding:12px 0 12px 12px;font-size:13px}
main{max-width:1056px;margin:auto;padding:34px 28px 48px}.page-heading{display:flex;justify-content:space-between;align-items:center;gap:16px;margin-bottom:20px}.eyebrow{font-size:12px;color:var(--muted);margin:0 0 4px;letter-spacing:.08em}h1{font-size:28px;line-height:1.35;letter-spacing:-.04em;margin:0;font-weight:650;overflow-wrap:anywhere}h2{font-size:14px;font-weight:650;margin:26px 0 12px}.subtitle{color:var(--muted);margin:-8px 0 24px}.live{font-size:12px;color:var(--muted);display:flex;align-items:center;gap:7px}.live:before{content:"";height:5px;width:5px;background:var(--accent);border-radius:50%}.muted,.meta{color:var(--muted);font-size:12px}.meta{overflow-wrap:anywhere}.stats{display:grid;grid-template-columns:1fr 1fr;gap:12px}.stat{display:flex;align-items:center;gap:14px;border:1px solid var(--line);background:var(--card);border-radius:9px;padding:12px 18px;min-height:68px}.stat>span:first-child{color:var(--muted);font-size:13px}.stat b{font-size:27px;font-weight:600;line-height:1;font-variant-numeric:tabular-nums}.arrow{margin-left:auto;color:var(--muted)}.stat:hover,.person:hover{border-color:var(--accent)}
.section-heading{display:flex;justify-content:space-between;align-items:center;margin:28px 0 12px}.section-heading h2{margin:0}.section-heading h2 span{color:var(--muted);font-weight:400;margin-left:6px}.goals{display:grid;grid-template-columns:1fr 1fr;gap:12px;align-items:start}.goal{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:12px 16px 6px;min-width:0}.goal-head{display:flex;align-items:center;justify-content:space-between;gap:8px}.node-link{display:flex;gap:9px;align-items:center;min-width:0;min-height:32px}.node-link span{overflow-wrap:anywhere}.goal-head>.node-link{font-size:15px;font-weight:600}.node-icon{height:19px;width:19px;flex-shrink:0;color:var(--accent);vertical-align:middle}.tags{display:flex;gap:4px;flex-wrap:wrap;justify-content:flex-end}.tag{display:inline-block;font-size:10px;line-height:1.8;white-space:nowrap;color:var(--muted);padding:1px 6px;background:var(--soft);border-radius:4px;font-weight:400}.tag.status,.tag.owner{color:var(--accent)}.goal-details summary{font-size:11px;margin:0;padding:3px 0 5px 28px;min-height:28px}.goal-details summary span{margin-left:4px;font-variant-numeric:tabular-nums}.goal-hint{font-size:11px;color:var(--muted);margin:3px 0 5px 28px;min-height:20px}.kids,.line-items{list-style:none;margin:6px 0 8px;padding:4px 0 0 12px;border-left:1px solid var(--line)}.kids>li{padding:5px 0;font-size:13px}.kids .tag{margin:0 0 0 28px}.line-items{margin-left:9px}.line-items .node-icon{width:15px;height:15px;color:var(--muted)}.shared{display:block;font-size:10px;color:var(--accent);margin-left:28px;overflow-wrap:anywhere}.gate{padding:8px 0;border-top:1px solid var(--line);margin:4px 0}
.card{border:1px solid var(--line);background:var(--card);border-radius:10px;padding:20px;margin:12px 0}.card.error{color:var(--danger);border-color:var(--danger)}.list{list-style:none;padding:0;margin:0;border:1px solid var(--line);border-radius:10px;background:var(--card);overflow:hidden}.list>li{padding:12px 16px;overflow-wrap:anywhere}.list>li+li{border-top:1px solid var(--line)}.list>li>a{display:block}.list>li>a.node-link{display:flex}.message-preview{display:flex;justify-content:space-between;gap:12px;margin-top:4px}.latest a{min-height:44px}.people{display:grid;grid-template-columns:repeat(3,1fr);gap:10px}.person{padding:12px 16px;border:1px solid var(--line);border-radius:9px;background:var(--card);display:flex;align-items:center;gap:12px;min-width:0}.person b{font-size:13px;font-weight:550}.person small{display:block;color:var(--muted);font-size:11px;overflow-wrap:anywhere}.person .node-icon{width:26px;height:26px}.footer{border-top:1px solid var(--line);padding-top:18px;margin-top:28px;font-size:11px}
.filters{display:flex;gap:4px;margin-bottom:20px}.filters a{min-height:44px;padding:10px 14px;border-radius:7px;color:var(--muted);font-size:13px}.filters a.selected{background:var(--soft);color:var(--accent);font-weight:600}.message,.proposal{padding:20px 22px}.message.unread{border-left:3px solid var(--accent)}.message-head{display:flex;align-items:baseline;justify-content:space-between;gap:12px;margin-bottom:5px;overflow-wrap:anywhere}.message-head b{font-size:13px;font-weight:550;min-width:0}.message-head time{flex-shrink:0;font-size:11px}.message .meta a{color:var(--accent);margin-right:6px}.task{font-size:16px;line-height:1.5;font-weight:600;margin:16px 0 10px;color:var(--fg);overflow-wrap:anywhere}.message-body{border-top:1px solid var(--line);margin-top:16px}summary{cursor:pointer;color:var(--muted);font-size:12px;min-height:44px;padding:12px 0}summary:hover{color:var(--accent)}.actions{display:flex;align-items:center;gap:10px;flex-wrap:wrap;margin-top:14px}.actions form{margin:0}.branch-name,.files{overflow-wrap:anywhere;font:11px/1.7 ui-monospace,SFMono-Regular,Menlo,monospace;color:var(--muted)}.files{background:var(--code);padding:8px 10px;border-radius:5px}.empty{text-align:center;padding:44px 20px}.empty h2{margin:0 0 8px;color:var(--fg)}
button{font:inherit;font-size:13px;font-weight:550;min-height:44px;padding:10px 16px;border-radius:7px;border:1px solid var(--accent);background:var(--accent);color:var(--card);cursor:pointer}button:hover{filter:brightness(.94)}button.ghost{color:var(--fg);background:transparent;border-color:var(--line)}button.danger{color:var(--danger)}button.small{font-size:12px}.back{display:inline-flex;align-items:center;min-height:44px;color:var(--muted);font-size:12px;margin-bottom:12px}.item-tags{margin:-8px 0 18px}.item-tags .tag{margin-right:6px}.file-title{font-size:21px}.login{max-width:420px;margin:60px auto}.login .subtitle{margin:12px 0 24px}.login label{font-size:13px}.login button{width:100%;margin:12px 0 0}input{font:inherit;width:100%;min-height:44px;padding:10px;border-radius:7px;border:1px solid var(--line);background:var(--bg);color:var(--fg);margin:8px 0}pre{white-space:pre-wrap;overflow-wrap:anywhere;font:12px/1.8 ui-monospace,SFMono-Regular,Menlo,monospace;margin:0;background:var(--code);padding:14px;border-radius:6px}pre.diff{max-height:50vh;overflow:auto}pre.doc{max-height:70vh;overflow:auto}article[id]{scroll-margin-top:16px}
@media(max-width:640px){.topbar-inner{padding:12px 16px 0;gap:8px;flex-wrap:wrap;min-height:92px}.brand{font-size:13px;order:0}.brand-mark{width:21px;height:21px;font-size:14px}.logout{order:1;padding:0 0 0 12px;min-height:32px;display:flex;align-items:center}nav{order:2;width:100%;gap:0}nav a{flex:1;text-align:center;border-radius:7px 7px 0 0;padding:10px 8px}main{padding:22px 16px 32px}.page-heading{margin-bottom:16px}h1{font-size:25px}.eyebrow{font-size:10px}.live{font-size:10px}.stat{gap:10px;min-height:60px;padding:10px 12px}.stat b{font-size:24px}.stat>span:first-child{font-size:12px}.stats{gap:8px}.section-heading{margin-top:22px;margin-bottom:10px}.section-heading>.muted{font-size:10px}.goals{grid-template-columns:1fr;gap:8px}.goal{padding:8px 12px 3px}.goal-head>.node-link{font-size:14px;min-height:28px}.tag{font-size:9px}.goal-details summary{min-height:25px;padding-top:2px;padding-bottom:3px}.goal-hint{min-height:18px;margin-top:2px;margin-bottom:3px}.goal-head{gap:5px}.goal-details[open]{padding-bottom:8px}.people{grid-template-columns:1fr 1fr;gap:8px}.person{padding:10px;gap:8px}.person .node-icon{width:20px;height:20px}.card,.message,.proposal{padding:16px}.message-head{gap:8px}.message-head b{font-size:12px}.task{font-size:15px}.login{margin:32px auto}.footer{line-height:1.9}.file-title{font-size:18px}}

[hidden]{display:none!important}
.view-switch{display:flex;padding:3px;border:1px solid var(--line);border-radius:8px;background:var(--card);gap:2px}
.view-switch button{min-height:32px;border:0;background:transparent;color:var(--muted);padding:5px 13px;font-size:12px;border-radius:5px;font-weight:500}
.view-switch button[aria-pressed="true"]{background:var(--soft);color:var(--accent)}
.goal-map{--map-wire:#b9c9be;--map-gate:#b09a77;background:var(--card);border:1px solid var(--line);border-radius:12px;overflow:hidden}
.map-heading{padding:25px 28px 22px;display:flex;align-items:center;justify-content:space-between;gap:20px;border-bottom:1px solid var(--line)}
.map-heading .eyebrow{font-size:10px;letter-spacing:.16em;margin-bottom:7px}
.map-heading h2{font:500 23px/1.5 "Songti SC","STSong",serif;letter-spacing:.05em;margin:0}
.map-description{font-size:11px;color:var(--muted);margin:8px 0 0}
.map-filter{display:flex;flex-direction:column;gap:5px;flex-shrink:0}.map-filter label{font-size:10px;color:var(--muted)}
.map-filter select{font:inherit;font-size:12px;color:var(--fg);border:1px solid var(--line);border-radius:7px;padding:9px 30px 9px 10px;min-height:40px;background:var(--card);max-width:230px}
.map-viewport{overflow:auto;padding:0 20px;outline-offset:-3px}.map-plane{position:relative;display:grid;grid-template-columns:1fr .83fr 1.05fr;column-gap:9%;min-width:730px;padding:0 16px 24px}
.map-column{min-width:0;position:relative}.map-column h3{display:flex;align-items:center;gap:9px;font-size:11px;font-weight:500;color:var(--muted);margin:0;height:62px;letter-spacing:.08em}
.map-column h3>span{font:11px ui-monospace,SFMono-Regular,Menlo,monospace;color:var(--muted);opacity:.65}
.map-nodes{height:var(--map-height);position:relative}
.map-node{position:absolute;top:var(--node-top);left:0;width:100%;min-height:72px;padding:13px 12px 11px;border:1px solid var(--line);background:var(--card);border-radius:8px;z-index:1;transition:border-color .18s,opacity .18s}
.map-node-title{display:flex;align-items:center;gap:8px;font-weight:550;font-size:12px;line-height:1.5;min-height:20px}
.map-node-title>span:not(.map-open){overflow-wrap:anywhere;min-width:0}.map-node-title .node-icon{width:16px;height:16px}.map-open{margin-left:auto;font-size:11px;font-weight:400;color:var(--muted);opacity:0}
.map-node.is-selected{border-color:var(--accent);background:var(--soft)}
.map-node:hover,.map-node:focus-visible{border-color:var(--accent);color:var(--fg)}.map-node:hover .map-open,.map-node:focus-visible .map-open{opacity:1}
.map-node-meta{display:flex;align-items:center;flex-wrap:wrap;gap:5px;margin:7px 0 0 24px;min-height:16px;font-size:10px;color:var(--muted)}.map-node-meta .tag{font-size:9px;padding:0;background:transparent}.map-node-meta .status{background:var(--soft);padding:0 5px}
.map-line{background:var(--soft);border-color:var(--map-wire)}.map-line .map-node-meta{color:var(--accent)}.map-item .node-icon{color:var(--muted)}.map-wait{color:var(--map-gate)}
.map-edges{position:absolute;inset:0;width:100%;height:100%;overflow:visible;pointer-events:none}
.map-edges path{fill:none;stroke:var(--map-wire);stroke-width:1.3;vector-effect:non-scaling-stroke;transition:opacity .18s,stroke .18s}
.map-edges path[data-kind="prerequisite"]{stroke:var(--map-gate);stroke-dasharray:4 5;marker-end:url(#map-prerequisite-arrow)}
.map-edges path.is-highlighted{stroke:var(--accent);stroke-width:1.8}.map-edges path.is-muted,.map-node.is-muted{opacity:.22}
.map-footer{display:flex;align-items:center;justify-content:space-between;gap:12px;border-top:1px solid var(--line);padding:14px 28px;font-size:10px;color:var(--muted)}
.map-legend{display:flex;flex-wrap:wrap;gap:16px}.map-legend>span{display:flex;align-items:center;gap:6px}.map-legend i{width:20px;border-top:1px solid var(--map-wire)}.map-legend i.dashed{border-color:var(--map-gate);border-top-style:dashed}
.map-plane.is-filtered .map-nodes{height:auto;display:flex;flex-direction:column;gap:18px;padding-bottom:10px}.map-plane.is-filtered .map-node{position:relative;top:auto}.map-plane.is-filtered{align-items:center;min-height:250px}.map-plane.is-filtered .map-column{align-self:center}
@media(prefers-color-scheme:dark){.goal-map{--map-wire:#526f5e;--map-gate:#bba078}}
@media(prefers-reduced-motion:reduce){.map-node,.map-edges path{transition:none}}
@media(max-width:640px){.view-switch button{min-height:38px;padding:6px 12px}.goal-map{border-radius:10px}.map-heading{padding:20px 18px;display:block}.map-heading h2{font-size:23px}.map-filter{margin-top:16px;flex-direction:row;align-items:center;gap:12px}.map-filter select{flex:1;min-width:0;min-height:44px;max-width:none;font-size:13px}.map-filter label{font-size:11px}.map-viewport{padding:0 12px}.map-plane.is-filtered{display:flex;flex-direction:column;align-items:stretch;min-width:0;padding:0 0 20px 22px;gap:8px}.map-plane.is-filtered .map-column{align-self:stretch}.map-plane.is-filtered .map-column h3{height:46px}.map-plane.is-filtered .map-nodes{gap:14px;padding:0}.map-plane.is-filtered .map-node{min-height:74px}.map-node-title{font-size:13px}.map-node-meta{font-size:11px}.map-node-meta .tag{font-size:10px}.map-open{opacity:1}.map-footer{align-items:flex-start;flex-direction:column;padding:14px 18px;gap:9px}.map-legend{gap:14px}.map-description{font-size:11px}}
{{THREAD_CSS}}
</style></head><body>
<div class="topbar"><div class="topbar-inner"><a class="brand" href="/d/"><span class="brand-mark" aria-hidden="true">＋</span>共享空间</a>{{NAV}}</div></div><main>{{BODY}}</main>
<script>
(() => {
 const map = document.getElementById('goal-map');
 if (!map) return;
 const switcher = document.querySelector('.view-switch');
 const list = document.getElementById('goal-list');
 const plane = map.querySelector('.map-plane');
 const svg = map.querySelector('.map-edges');
 const select = document.getElementById('map-focus');
 const nodes = [...map.querySelectorAll('[data-node]')];
 const byID = new Map(nodes.map(node => [node.dataset.node, node]));
 const edges = [...svg.querySelectorAll('path')];
 const narrow = matchMedia('(max-width:640px)');
 let queued = false;

 // Rounded detours keep a direct goal-to-item edge out of intermediate cards.
 function roundedPath(points) {
  let d = 'M ' + points[0].join(' ');
  for (let i = 1; i < points.length - 1; i++) {
   const [x,y] = points[i], [px,py] = points[i-1], [nx,ny] = points[i+1];
   const before = Math.hypot(x-px,y-py), after = Math.hypot(nx-x,ny-y);
   if (!before || !after) continue;
   const r = Math.min(10,before/2,after/2);
   d += ' L ' + (x+(px-x)*r/before) + ' ' + (y+(py-y)*r/before) + ' Q ' + x + ' ' + y + ', ' + (x+(nx-x)*r/after) + ' ' + (y+(ny-y)*r/after);
  }
  return d + ' L ' + points[points.length-1].join(' ');
 }
 // Use real element bounds, including wrapped titles and system font metrics.
 function draw() {
  queued = false;
  if (map.hidden) return;
  if (!plane.classList.contains('is-filtered')) {
   let height = 0;
   plane.querySelectorAll('.map-nodes').forEach(column => {
    let bottom = 0;
    [...column.children].forEach(node => {
     if (node.hidden) return;
     const top = Math.max(parseFloat(node.style.getPropertyValue('--node-top')), bottom);
     node.style.top = top + 'px';
     bottom = top + node.offsetHeight + 20;
    });
    height = Math.max(height, bottom);
   });
   plane.style.setProperty('--map-height', height + 'px');
  }
  const origin = plane.getBoundingClientRect();
  svg.setAttribute('viewBox', '0 0 ' + origin.width + ' ' + origin.height);
  const vertical = narrow.matches && plane.classList.contains('is-filtered');
  edges.forEach(edge => {
   const from = byID.get(edge.dataset.from), to = byID.get(edge.dataset.to);
   if (!from || !to || from.hidden || to.hidden) { edge.setAttribute('d', ''); return; }
   const a = from.getBoundingClientRect(), b = to.getBoundingClientRect();
   const ay = a.top + a.height / 2 - origin.top, by = b.top + b.height / 2 - origin.top;
   const sameColumn = from.closest('.map-column') === to.closest('.map-column');
   let d;
   if (vertical || sameColumn) {
    const ax = a.left - origin.left, bx = b.left - origin.left;
    const lane = Math.max(3, Math.min(ax, bx) - (edge.dataset.kind === 'prerequisite' ? 17 : 12));
    d = 'M ' + ax + ' ' + ay + ' C ' + lane + ' ' + ay + ', ' + lane + ' ' + ay + ', ' + lane + ' ' + (ay + (by > ay ? 14 : -14)) + ' L ' + lane + ' ' + (by + (by > ay ? -14 : 14)) + ' Q ' + lane + ' ' + by + ', ' + bx + ' ' + by;
   } else {
    const forward = a.left < b.left;
    const ax = (forward ? a.right : a.left) - origin.left, bx = (forward ? b.left : b.right) - origin.left;
    const bend = (bx - ax) * .48;
    d = 'M ' + ax + ' ' + ay + ' C ' + (ax + bend) + ' ' + ay + ', ' + (bx - bend) + ' ' + by + ', ' + bx + ' ' + by;
    const obstacles = nodes.filter(node => !node.hidden && node !== from && node !== to).map(node => node.getBoundingClientRect()).filter(rect => rect.left > Math.min(a.right,b.right) && rect.right < Math.max(a.left,b.left)).map(rect => ({left:rect.left-origin.left,right:rect.right-origin.left,top:rect.top-origin.top,bottom:rect.bottom-origin.top}));
    const blocked = obstacles.some(rect => {
     for (let step = 0; step <= 40; step++) {
      const t = step/40, u = 1-t;
      const x = u*u*u*ax + 3*u*u*t*(ax+bend) + 3*u*t*t*(bx-bend) + t*t*t*bx;
      const y = u*u*u*ay + 3*u*u*t*ay + 3*u*t*t*by + t*t*t*by;
      if (x > rect.left-6 && x < rect.right+6 && y > rect.top-6 && y < rect.bottom+6) return true;
     }
     return false;
    });
    if (blocked) {
     const candidates = obstacles.flatMap(rect => [rect.top-14,rect.bottom+14]);
     const lanes = candidates.filter(y => obstacles.every(rect => y < rect.top-8 || y > rect.bottom+8));
     lanes.sort((x,y) => Math.abs(x-(ay+by)/2)-Math.abs(y-(ay+by)/2));
     const lane = lanes[0];
     const left = (Math.min(ax,bx) + Math.min(...obstacles.map(rect => rect.left)))/2;
     const right = (Math.max(ax,bx) + Math.max(...obstacles.map(rect => rect.right)))/2;
     const startLane = forward ? left : right, endLane = forward ? right : left;
     d = roundedPath([[ax,ay],[startLane,ay],[startLane,lane],[endLane,lane],[endLane,by],[bx,by]]);
    }
   }
   edge.setAttribute('d', d);
  });
 }
 function scheduleDraw() { if (!queued) { queued = true; requestAnimationFrame(draw); } }
 function filter() {
  const selected = select.value;
  let visible = new Set(nodes.map(node => node.dataset.node));
  if (selected !== 'all') {
   visible = new Set([selected]);
   // Descendants first; then include their co-parents and prerequisite chains.
   let changed = true;
   while (changed) {
    changed = false;
    edges.filter(edge => edge.dataset.kind === 'serves').forEach(edge => {
     if (visible.has(edge.dataset.from) && !visible.has(edge.dataset.to)) { visible.add(edge.dataset.to); changed = true; }
    });
   }
   changed = true;
   while (changed) {
    changed = false;
    edges.forEach(edge => {
     if (visible.has(edge.dataset.to) && !visible.has(edge.dataset.from)) { visible.add(edge.dataset.from); changed = true; }
    });
   }
  }
  nodes.forEach(node => { node.hidden = !visible.has(node.dataset.node); node.style.top = ''; node.classList.toggle('is-selected', node.dataset.node === selected); });
  plane.classList.toggle('is-filtered', selected !== 'all');
  plane.querySelectorAll('.map-column').forEach(column => {
   column.hidden = selected !== 'all' && ![...column.querySelectorAll('[data-node]')].some(node => !node.hidden);
  });
  map.querySelector('.map-help').textContent = narrow.matches && selected === 'all' ? '左右滑动查看全图 · 点击节点查看详情' : '点击节点查看详情';
  scheduleDraw();
 }
 function highlight(id) {
  const adjacent = new Set([id]);
  edges.forEach(edge => { if (edge.dataset.from === id || edge.dataset.to === id) { adjacent.add(edge.dataset.from); adjacent.add(edge.dataset.to); } });
  nodes.forEach(node => node.classList.toggle('is-muted', !!id && !adjacent.has(node.dataset.node)));
  edges.forEach(edge => {
   const active = edge.dataset.from === id || edge.dataset.to === id;
   edge.classList.toggle('is-muted', !!id && !active);
   edge.classList.toggle('is-highlighted', !!id && active);
  });
 }
 nodes.forEach(node => {
  node.addEventListener('pointerenter', () => { if (matchMedia('(hover:hover)').matches) highlight(node.dataset.node); });
  node.addEventListener('pointerleave', () => highlight(null));
  node.addEventListener('focus', () => highlight(node.dataset.node));
  node.addEventListener('blur', () => highlight(null));
 });
 switcher.hidden = false;
 switcher.querySelectorAll('button').forEach(button => button.addEventListener('click', () => {
  const showMap = button.dataset.view === 'goal-map';
  map.hidden = !showMap; list.hidden = showMap;
  switcher.querySelectorAll('button').forEach(other => other.setAttribute('aria-pressed', String(other === button)));
  scheduleDraw();
 }));
 if (narrow.matches && nodes.length) select.value = map.dataset.defaultGoal;
 select.addEventListener('change', filter);
 narrow.addEventListener('change', filter);
 new ResizeObserver(scheduleDraw).observe(plane);
 filter();
})();
</script>
</body></html>`

package web

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ketchupz1999/agents-home-public/internal/agents"
	"github.com/ketchupz1999/agents-home-public/internal/protocol"
)

//go:embed team.css
var teamCSS string

//go:embed team.js
var teamJS string

// IdentityAccess 仅向 owner 页面提供权限摘要，不包含凭据或哈希。
type IdentityAccess struct {
	ID             string
	Read           []string
	Owner, Approve bool
}

type teamNode struct {
	Key, ID    string
	Role       *agents.Role
	Identity   agentIdentity
	Principals []string
	Historical bool
	Registered bool
	// Derived nodes exist only because old messages name them (deleted or
	// renamed identities); they are hidden unless ShowHistory is set.
	Derived bool
}
type teamSnapshot struct {
	Registry              *agents.Registry
	Nodes                 []*teamNode
	ByKey                 map[string]*teamNode
	AllMessages, Messages []protocol.Message
	MailErr               error
	Since, Until          time.Time
	Range, View           string
	RefreshErr            error
	ShowHistory           bool
}

var teamZone = time.FixedZone("UTC+8", 8*60*60)

func teamTime(t time.Time) string {
	if t.IsZero() {
		return "未观测"
	}
	return t.In(teamZone).Format("01/02 15:04")
}
func parseTeamTime(s string) time.Time { t, _ := time.Parse(time.RFC3339Nano, s); return t }
func (w *Web) teamNow() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

// loadTeam 固定一次请求的角色快照与时间窗口；信箱失败不能变成零次往来。
func (w *Web) loadTeam(r *http.Request) (*teamSnapshot, error) {
	refreshErr := w.Store.Refresh()
	reg, err := agents.Load(w.Store)
	if err != nil {
		return nil, fmt.Errorf("角色配置暂时无法读取")
	}
	t := &teamSnapshot{RefreshErr: refreshErr, Registry: reg, ByKey: map[string]*teamNode{}, Until: w.teamNow(), Range: r.URL.Query().Get("range"), View: r.URL.Query().Get("view")}
	switch t.Range {
	case "":
		t.Range = "7"
	case "7", "30", "all":
	default:
		return nil, fmt.Errorf("无效的时间范围")
	}
	if s := r.URL.Query().Get("at"); s != "" {
		at, e := time.Parse(time.RFC3339Nano, s)
		if e != nil || at.After(t.Until.Add(time.Minute)) {
			return nil, fmt.Errorf("无效的快照时间")
		}
		t.Until = at.UTC()
	}
	if t.Range != "all" {
		days, _ := strconv.Atoi(t.Range)
		t.Since = t.Until.Add(-time.Duration(days) * 24 * time.Hour)
	}
	t.ShowHistory = r.URL.Query().Get("history") == "1"
	switch t.View {
	case "":
		t.View = "mail"
	case "mail", "data", "activity":
	default:
		return nil, fmt.Errorf("无效的视图")
	}
	for i := range reg.Roles {
		role := &reg.Roles[i]
		if role.ID == "owner" {
			continue
		}
		n := &teamNode{Key: "r:" + role.ID, ID: role.ID, Role: role, Identity: w.identityAt(reg, role.ID, role.ID)}
		t.addNode(n)
	}
	for _, p := range reg.Agents {
		if p.ID == "owner" {
			continue
		}
		key := t.principalKey(p.ID)
		n := t.ByKey[key]
		if n == nil {
			n = &teamNode{Key: key, ID: p.ID, Identity: w.identityAt(reg, p.ID, "")}
			t.addNode(n)
		}
		n.Principals = append(n.Principals, p.ID)
		n.Registered = true
	}
	t.AllMessages, t.MailErr = w.DB.ListMessages(w.owner(), "all", "", false, 0)
	if t.MailErr != nil {
		return t, nil
	}
	for _, m := range t.AllMessages {
		if !m.CreatedAt.Before(t.Until) {
			continue
		}
		ends := []struct{ key, id, role string }{{t.senderKey(m), reg.Canonical(m.From), m.FromRole}}
		if !isGroupMessage(m) {
			ends = append(ends, struct{ key, id, role string }{t.principalKey(m.To), reg.Canonical(m.To), ""})
		}
		for _, a := range ends {
			if a.id == "owner" {
				continue
			}
			if t.ByKey[a.key] == nil {
				n := &teamNode{Key: a.key, ID: a.id, Identity: w.identityAt(reg, a.id, ""), Derived: true}
				if a.role != "" {
					n.ID = a.role
					n.Historical = true
					n.Identity = defaultIdentity(a.role)
				} else {
					n.Principals = []string{a.id}
				}
				t.addNode(n)
			}
		}
		if t.Since.IsZero() || !m.CreatedAt.Before(t.Since) {
			t.Messages = append(t.Messages, m)
		}
	}
	return t, nil
}
func (t *teamSnapshot) addNode(n *teamNode) { t.ByKey[n.Key] = n; t.Nodes = append(t.Nodes, n) }

// isGroupMessage: a group is a room, not a teammate, so group traffic never
// becomes a node or an edge on the squad map.
func isGroupMessage(m protocol.Message) bool {
	return m.GroupID != "" || strings.HasPrefix(m.To, "group:")
}

// visible reports whether a node is drawn under the current history setting.
func (t *teamSnapshot) visible(key string) bool {
	n := t.ByKey[key]
	return n != nil && (t.ShowHistory || !n.Derived)
}

// shownNodes is the node list after the history toggle.
func (t *teamSnapshot) shownNodes() []*teamNode {
	out := []*teamNode{}
	for _, n := range t.Nodes {
		if t.ShowHistory || !n.Derived {
			out = append(out, n)
		}
	}
	return out
}

func (t *teamSnapshot) hiddenCount() int {
	c := 0
	for _, n := range t.Nodes {
		if n.Derived {
			c++
		}
	}
	return c
}

func (t *teamSnapshot) principalKey(id string) string {
	id = t.Registry.Canonical(id)
	if id == "owner" {
		return "p:owner"
	}
	if role := t.Registry.RoleFor(id); role != "" {
		return "r:" + role
	}
	return "p:" + id
}
func (t *teamSnapshot) senderKey(m protocol.Message) string {
	if m.From == "owner" {
		return "p:owner"
	}
	if m.FromRole != "" {
		// A frozen role that was later renamed resolves through aliases;
		// an unknown former role stays a separate historical node.
		if c := t.Registry.Canonical(m.FromRole); c != m.FromRole {
			return t.principalKey(c)
		}
		if t.Registry.Canonical(m.From) != m.From && !t.Registry.HasRole(m.FromRole) {
			return t.principalKey(m.From)
		}
		return "r:" + m.FromRole
	}
	return t.principalKey(m.From)
}
func (t *teamSnapshot) label(key string) string {
	if n := t.ByKey[key]; n != nil {
		return n.Identity.Name
	}
	if key == "p:owner" {
		return "本人"
	}
	return strings.TrimPrefix(strings.TrimPrefix(key, "r:"), "p:")
}
func (t *teamSnapshot) query() url.Values {
	q := url.Values{"range": {t.Range}, "at": {t.Until.Format(time.RFC3339Nano)}}
	if t.ShowHistory {
		q.Set("history", "1")
	}
	return q
}
func teamURL(path string, q url.Values) string {
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}
func cloneQuery(q url.Values) url.Values {
	out := url.Values{}
	for k, v := range q {
		out[k] = append([]string(nil), v...)
	}
	return out
}
func (t *teamSnapshot) nodeURL(n *teamNode, tab string) string {
	q := t.query()
	if strings.HasPrefix(n.Key, "p:") {
		q.Set("identity", "1")
	}
	if tab != "" {
		q.Set("tab", tab)
	}
	return teamURL("/d/agents/"+url.PathEscape(n.ID), q)
}
func teamStatus(n *teamNode) string {
	if n.Historical {
		return "历史角色"
	}
	if !n.Registered && len(n.Principals) == 0 {
		return "尚未接入"
	}
	if n.Role == nil && !n.Registered {
		return "未归属身份"
	}
	if n.Role == nil {
		return "独立身份"
	}
	return "已登记"
}

type teamStats struct {
	Participated, Started, Replied, Partners int
	Last                                     time.Time
}

func (t *teamSnapshot) stats(n *teamNode, includeOwner bool) teamStats {
	var s teamStats
	peers := map[string]bool{}
	for _, m := range t.Messages {
		if !includeOwner && (m.From == "owner" || m.To == "owner") {
			continue
		}
		from, to := t.senderKey(m), t.principalKey(m.To)
		if isGroupMessage(m) {
			to = ""
		}
		if from != n.Key && to != n.Key {
			continue
		}
		s.Participated++
		if m.CreatedAt.After(s.Last) {
			s.Last = m.CreatedAt
		}
		if from == n.Key {
			if m.ReplyTo == "" {
				s.Started++
			} else {
				s.Replied++
			}
		}
		for _, p := range []string{from, to} {
			if p != n.Key && p != "" {
				peers[p] = true
			}
		}
	}
	s.Partners = len(peers)
	return s
}

type teamEdge struct {
	From, To string
	Messages []protocol.Message
}

func (t *teamSnapshot) edges() []teamEdge {
	indices := map[string]int{}
	out := []teamEdge{}
	for _, m := range t.Messages {
		if m.From == "owner" || m.To == "owner" || isGroupMessage(m) {
			continue
		}
		a, b := t.senderKey(m), t.principalKey(m.To)
		if a == b || !t.visible(a) || !t.visible(b) {
			continue
		}
		if a > b {
			a, b = b, a
		}
		key := a + "," + b
		i, ok := indices[key]
		if !ok {
			i = len(out)
			indices[key] = i
			out = append(out, teamEdge{From: a, To: b})
		}
		out[i].Messages = append(out[i].Messages, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Messages) != len(out[j].Messages) {
			return len(out[i].Messages) > len(out[j].Messages)
		}
		return out[i].From+out[i].To < out[j].From+out[j].To
	})
	return out
}
func teamBundle(v agentIdentity) (characterBundle, bool) {
	if v.Character != "" {
		for _, b := range characterBundles {
			if b.ID == v.Character {
				return b, true
			}
		}
		return characterBundle{}, false
	}
	b, ok := characterBundles[v.Kind]
	return b, ok && v.ID != "owner"
}
func teamAvatar(v agentIdentity) string {
	if b, ok := teamBundle(v); ok {
		return fmt.Sprintf(`<span class="team-actor %s" data-manifest="%s" data-character="%s"><span class="actor-fallback">%s</span><canvas width="160" height="160" hidden aria-hidden="true"></canvas></span>`, esc(b.ID), esc(b.Manifest), esc(b.ID), avatar(v))
	}
	if v.Character == "placeholder" {
		return `<span class="team-actor"><span class="finance-placeholder" aria-hidden="true">↗</span></span>`
	}
	return `<span class="team-actor">` + avatar(v) + `</span>`
}
func teamShell(rw http.ResponseWriter, title, body string) {
	page(rw, title, "/d/agents", `<style>`+teamCSS+`</style><div class="team-page">`+body+`</div><script>`+teamJS+`</script>`)
}
func (w *Web) agents(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Cache-Control", "no-store")
	t, err := w.loadTeam(r)
	if err != nil {
		http.Error(rw, err.Error(), 503)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<header class="team-heading"><h1>我的小队</h1><span>更新于 %s</span></header>`, esc(teamTime(t.Until)))
	if t.RefreshErr != nil {
		b.WriteString(`<p class="team-error">共享资料刷新失败；角色使用最近已知的已发布快照。</p>`)
	}
	b.WriteString(teamToolbar(t, r))
	if t.View == "activity" {
		b.WriteString(w.renderTeamActivity(t, r, nil))
	} else {
		b.WriteString(`<div class="team-workspace"><section class="team-panel">`)
		if t.View == "mail" {
			b.WriteString(w.renderMailMap(t, r))
		} else {
			b.WriteString(w.renderDataMap(t, r))
		}
		b.WriteString(`</section><aside class="team-detail" id="team-detail">`)
		if t.View == "mail" && r.URL.Query().Get("pair") != "" {
			b.WriteString(w.pairDetail(t, r))
		} else {
			n := t.ByKey[r.URL.Query().Get("node")]
			if shown := t.shownNodes(); n == nil && len(shown) > 0 {
				n = shown[0]
			}
			if n == nil {
				b.WriteString(`<p class="team-empty">尚未登记 Agent</p>`)
			} else {
				b.WriteString(w.teamNodeDetail(t, n))
			}
		}
		b.WriteString(`</aside></div>`)
	}
	b.WriteString(`<details class="team-notes"><summary>统计口径</summary><p>发起新话题按无 reply_to 的信件统计，不代表自主触发；回复不等于完成任务。交流图排除本人往来和群组消息，不绘制同角色内部通信；已删除或改名、只在旧消息里出现的代号默认折叠；在 registry 的 aliases 里登记旧代号即可并入现身份。没有观测记录时显示未知。</p><p>角色名称用于展示；原始身份、投递地址和权限独立保留。资料版本表示服务实际返回的内容指纹，不保证 Agent 已理解或远端内容最新。</p></details>`)
	teamShell(rw, "我的小队", b.String())
}
func teamToolbar(t *teamSnapshot, r *http.Request) string {
	var b strings.Builder
	b.WriteString(`<div class="team-toolbar"><div class="team-tabs" aria-label="小队视图">`)
	for _, v := range []struct{ id, name string }{{"mail", "交流"}, {"data", "数据"}, {"activity", "活动"}} {
		q := t.query()
		q.Set("view", v.id)
		current := ""
		if t.View == v.id {
			current = ` aria-current="page"`
		}
		fmt.Fprintf(&b, `<a href="%s"%s>%s</a>`, esc(teamURL("/d/agents", q)), current, v.name)
	}
	b.WriteString(`</div>`)
	b.WriteString(teamRangeForm("/d/agents", t, r, url.Values{"view": {t.View}}))
	if hidden := t.hiddenCount(); hidden > 0 && t.View != "activity" {
		q := t.query()
		q.Set("view", t.View)
		label := fmt.Sprintf("显示历史身份（%d）", hidden)
		if t.ShowHistory {
			q.Del("history")
			label = "隐藏历史身份"
		} else {
			q.Set("history", "1")
		}
		fmt.Fprintf(&b, `<a class="team-text team-history" href="%s" aria-pressed="%t">%s</a>`, esc(teamURL("/d/agents", q)), t.ShowHistory, esc(label))
	}
	b.WriteString(`<button class="team-motion" id="team-motion" type="button" aria-pressed="true" hidden>暂停动效</button></div>`)
	return b.String()
}
func teamRangeForm(path string, t *teamSnapshot, r *http.Request, fields url.Values) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<form method="get" action="%s" class="team-range">`, esc(path))
	for k, vs := range fields {
		for _, v := range vs {
			fmt.Fprintf(&b, `<input type="hidden" name="%s" value="%s">`, esc(k), esc(v))
		}
	}
	b.WriteString(`<label>时间 <select name="range" aria-label="时间范围">`)
	for _, v := range []struct{ id, name string }{{"7", "近 7 天"}, {"30", "近 30 天"}, {"all", "全部"}} {
		sel := ""
		if t.Range == v.id {
			sel = " selected"
		}
		fmt.Fprintf(&b, `<option value="%s"%s>%s</option>`, v.id, sel, v.name)
	}
	b.WriteString(`</select></label><button class="team-text" type="submit">查看</button></form>`)
	return b.String()
}
func (w *Web) renderMailMap(t *teamSnapshot, r *http.Request) string {
	var b strings.Builder
	edges := t.edges()
	count := 0
	for _, e := range edges {
		count += len(e.Messages)
	}
	if t.MailErr != nil {
		b.WriteString(`<p class="team-error">信箱暂时无法读取，交流统计不可用。</p>`)
	} else {
		fmt.Fprintf(&b, `<div class="team-map-heading"><span>%d 条点对点关系 · %d 条历史消息</span><span>不含群组协作</span></div>`, len(edges), count)
	}
	b.WriteString(`<p class="team-note">此图只统计历史点对点往来。当前协作在<a href="/d/groups">群组</a>中进行，成员卡计数包含群内发言。</p>`)
	b.WriteString(`<div class="team-map" data-layout="mail"><svg class="team-wires" aria-hidden="true">`)
	for i, e := range edges {
		fmt.Fprintf(&b, `<path data-edge="%d" data-from="%s" data-to="%s"/>`, i, esc(e.From), esc(e.To))
	}
	b.WriteString(`</svg>`)
	for _, n := range t.shownNodes() {
		q := t.query()
		q.Set("node", n.Key)
		q.Set("view", "mail")
		s := t.stats(n, false)
		label := fmt.Sprintf("新话题 %d · 回复 %d", s.Started, s.Replied)
		if t.MailErr != nil {
			label = "暂时无法统计"
		} else if !n.Registered && len(n.Principals) == 0 && !n.Historical {
			label = "尚未接入"
		}
		cls := ""
		if !n.Registered {
			cls = " is-unregistered"
		}
		fmt.Fprintf(&b, `<a class="team-node%s" data-node="%s" href="%s#team-detail">%s<strong>%s</strong><small>%s</small><span class="team-tag">%s</span></a>`, cls, esc(n.Key), esc(teamURL("/d/agents", q)), teamAvatar(n.Identity), esc(n.Identity.Name), esc(teamSubtitle(n)), esc(label))
	}
	for i, e := range edges {
		q := t.query()
		q.Set("pair", e.From+","+e.To)
		q.Set("view", "mail")
		fmt.Fprintf(&b, `<a class="team-edge-label" data-edge="%d" href="%s#team-detail" aria-label="查看 %s 与 %s 的往来">%d 条往来 ↗</a>`, i, esc(teamURL("/d/agents", q)), esc(t.label(e.From)), esc(t.label(e.To)), len(e.Messages))
	}
	if len(t.shownNodes()) == 0 {
		b.WriteString(`<p class="team-empty">尚未登记 Agent</p>`)
	}
	b.WriteString(`</div><div class="team-legend">点击角色或连线查看详情；群组消息不计入两两往来</div>`)
	return b.String()
}
func (w *Web) teamNodeDetail(t *teamSnapshot, n *teamNode) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<p class="team-kicker">%s</p><h2>%s</h2>`, esc(teamStatus(n)), esc(n.Identity.Name))
	if t.View == "mail" {
		b.WriteString(teamStatMarkup(t, n, false))
	} else {
		b.WriteString(w.dataSummary(t, n))
	}
	b.WriteString(`<dl class="team-kv">`)
	if n.Role != nil {
		b.WriteString(teamKV("工作空间", workspaceSummary(n.Role)))
		b.WriteString(teamKV("职责标签", strings.Join(n.Role.Tags, " · ")))
	}
	b.WriteString(teamKV("凭据身份", strings.Join(n.Principals, " / ")))
	b.WriteString(teamKV("Harness", "按运行实例记录"))
	b.WriteString(`</dl>`)
	fmt.Fprintf(&b, `<a class="team-primary" href="%s">查看完整看板 ↗</a>`, esc(t.nodeURL(n, "activity")))
	return b.String()
}
func teamKV(k, v string) string {
	if v == "" {
		v = "未记录"
	}
	return `<div><dt>` + esc(k) + `</dt><dd>` + esc(v) + `</dd></div>`
}
func teamStatMarkup(t *teamSnapshot, n *teamNode, owner bool) string {
	if t.MailErr != nil {
		return `<p class="team-error">交流记录暂时无法读取</p>`
	}
	s := t.stats(n, owner)
	var b strings.Builder
	b.WriteString(`<div class="team-stats">`)
	for _, v := range []struct {
		name string
		n    int
	}{{"参与往来", s.Participated}, {"发起新话题", s.Started}, {"发送回复", s.Replied}, {"交流伙伴", s.Partners}} {
		value := strconv.Itoa(v.n)
		if !n.Registered && len(n.Principals) == 0 && !n.Historical {
			value = "—"
		}
		fmt.Fprintf(&b, `<div><b>%s</b><span>%s</span></div>`, value, v.name)
	}
	b.WriteString(`</div>`)
	return b.String()
}
func (w *Web) pairDetail(t *teamSnapshot, r *http.Request) string {
	var edge *teamEdge
	for _, e := range t.edges() {
		if e.From+","+e.To == r.URL.Query().Get("pair") {
			copy := e
			edge = &copy
			break
		}
	}
	if edge == nil {
		return `<p class="team-empty">当前范围没有这组往来。</p>`
	}
	e := *edge
	replied := map[string]bool{}
	for _, m := range t.AllMessages {
		if m.ReplyTo != "" && m.CreatedAt.Before(t.Until) {
			replied[m.ReplyTo] = true
		}
	}
	roots, replies, waiting, forward := 0, 0, 0, 0
	for _, m := range e.Messages {
		if t.senderKey(m) == e.From {
			forward++
		}
		if m.ReplyTo == "" {
			roots++
			if m.Kind == protocol.KindAsk && !replied[m.ID] {
				waiting++
			}
		} else {
			replies++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<p class="team-kicker">%d 条往来 · %d 个新话题</p><h2>%s<br>↔ %s</h2><div class="team-stats"><div><b>%d</b><span>回复</span></div><div><b>%d</b><span>请求未见直接回复</span></div></div><dl class="team-kv">`, len(e.Messages), roots, esc(t.label(e.From)), esc(t.label(e.To)), replies, waiting)
	b.WriteString(teamKV(t.label(e.From)+" → "+t.label(e.To), fmt.Sprintf("%d 条", forward)))
	b.WriteString(teamKV(t.label(e.To)+" → "+t.label(e.From), fmt.Sprintf("%d 条", len(e.Messages)-forward)))
	b.WriteString(`</dl><ul class="team-messages">`)
	for i := len(e.Messages) - 1; i >= 0 && i >= len(e.Messages)-4; i-- {
		m := e.Messages[i]
		fmt.Fprintf(&b, `<li><a href="%s">%s ↗</a><small>%s · %s</small></li>`, esc(messageURL(m)), esc(truncate(conversationTitle(m.Task), 70)), esc(t.label(t.senderKey(m))), esc(teamTime(m.CreatedAt)))
	}
	b.WriteString(`</ul>`)
	q := t.query()
	q.Set("view", "activity")
	q.Set("pair", e.From+","+e.To)
	fmt.Fprintf(&b, `<a class="team-link" href="%s">全部往来 ↗</a>`, esc(teamURL("/d/agents", q)))
	return b.String()
}

type teamCursor struct{ At, ID string }

func encodeTeamCursor(at, id string) string {
	b, _ := json.Marshal(teamCursor{at, id})
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodeTeamCursor(raw string) (teamCursor, error) {
	var c teamCursor
	if raw == "" {
		return c, nil
	}
	if len(raw) > 512 {
		return c, fmt.Errorf("invalid cursor")
	}
	b, e := base64.RawURLEncoding.DecodeString(raw)
	if e != nil {
		return c, e
	}
	e = json.Unmarshal(b, &c)
	if e != nil || c.ID == "" {
		return c, fmt.Errorf("invalid cursor")
	}
	if _, e = time.Parse(time.RFC3339Nano, c.At); e != nil {
		return c, e
	}
	return c, nil
}
func teamBefore(at time.Time, id string, c teamCursor) bool {
	if c.ID == "" {
		return true
	}
	t := parseTeamTime(c.At)
	return at.Before(t) || (at.Equal(t) && id < c.ID)
}
func teamPageNext(r *http.Request, values url.Values, label string) string {
	q := cloneQuery(r.URL.Query())
	for k, v := range values {
		q[k] = v
	}
	return `<a class="team-next" href="` + esc(teamURL(r.URL.Path, q)) + `">` + esc(label) + ` →</a>`
}

func teamSubtitle(n *teamNode) string {
	if n.Role != nil && len(n.Role.Tags) > 0 {
		return strings.Join(n.Role.Tags, " · ")
	}
	if n.Identity.Kind != "" {
		return n.Identity.Kind
	}
	return truncate(n.Identity.Bio, 28)
}

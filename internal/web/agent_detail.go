package web

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ketchupz1999/agents-home-public/internal/db"
	"github.com/ketchupz1999/agents-home-public/internal/protocol"
	"github.com/ketchupz1999/agents-home-public/internal/store"
)

// agentDetail 展示一个角色的属性、原凭据权限以及有来源的活动。
func (w *Web) agentDetail(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Cache-Control", "no-store")
	t, err := w.loadTeam(r)
	if err != nil {
		http.Error(rw, err.Error(), 503)
		return
	}
	id := r.PathValue("role")
	if !protocol.ValidName(id) {
		http.NotFound(rw, r)
		return
	}
	key := "r:" + id
	if r.URL.Query().Get("identity") == "1" {
		key = "p:" + id
	}
	n := t.ByKey[key]
	if n == nil && r.URL.Query().Get("identity") == "" {
		n = t.ByKey["p:"+id]
	}
	if n == nil {
		http.NotFound(rw, r)
		return
	}
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "activity"
	}
	switch tab {
	case "activity", "workspace", "data", "runtime", "meta":
	default:
		http.Error(rw, "无效的详情页", 400)
		return
	}
	owner := r.URL.Query().Get("scope") == "all"
	var b strings.Builder
	if t.RefreshErr != nil {
		b.WriteString(`<p class="team-error">共享资料刷新失败；角色使用最近已知的已发布快照。</p>`)
	}
	fmt.Fprintf(&b, `<a class="team-back" href="%s">← 返回我的小队</a><header class="agent-hero">%s<div><div class="agent-title"><h1>%s</h1><span class="team-tag">%s</span></div><p>%s</p><small>%s</small></div></header>`, esc(teamURL("/d/agents", t.query())), teamAvatar(n.Identity), esc(n.Identity.Name), esc(teamStatus(n)), esc(n.Identity.Bio), esc(n.ID))
	window := "近 " + t.Range + " 天"
	if t.Range == "all" {
		window = "全部记录"
	}
	scope := "Agent 间"
	if owner {
		scope = "包含本人"
	}
	b.WriteString(`<div class="agent-stat-row">` + teamStatMarkup(t, n, owner) + `<p class="team-stat-window">` + esc(scope+" · "+window) + `</p></div><div class="agent-workspace"><aside class="agent-basics"><h2>基本信息</h2><dl class="team-kv">`)
	if n.Role != nil {
		b.WriteString(teamKV("职责标签", strings.Join(n.Role.Tags, " · ")))
		b.WriteString(teamKV("工作空间", workspaceSummary(n.Role)))
		b.WriteString(teamKV("唤起方式（声明）", invocationLabel(n.Role.Invocation)))
	}
	b.WriteString(teamKV("原凭据身份", strings.Join(n.Principals, " / ")))
	s := t.stats(n, owner)
	last := teamTime(s.Last)
	if t.MailErr != nil {
		last = "暂时无法读取"
	}
	b.WriteString(teamKV("范围内最近信件", last))
	b.WriteString(teamKV("页面取数时间", teamTime(t.Until)))
	b.WriteString(`</dl></aside><section class="agent-main"><div class="team-tabs agent-tabs">`)
	for _, v := range []struct{ id, name string }{{"activity", "活动"}, {"workspace", "工作空间"}, {"data", "数据接入"}, {"runtime", "运行"}, {"meta", "全部属性"}} {
		current := ""
		if v.id == tab {
			current = ` aria-current="page"`
		}
		fmt.Fprintf(&b, `<a href="%s"%s>%s</a>`, esc(t.nodeURL(n, v.id)), current, v.name)
	}
	b.WriteString(`</div>`)
	switch tab {
	case "activity":
		b.WriteString(w.renderTeamActivity(t, r, n))
	case "workspace":
		b.WriteString(agentWorkspace(n))
	case "data":
		b.WriteString(w.agentData(t, n))
	case "runtime":
		b.WriteString(w.agentRuns(t, r, n))
	case "meta":
		b.WriteString(w.agentMeta(t, n))
	}
	b.WriteString(`</section></div><button class="team-motion" id="team-motion" type="button" aria-pressed="true" hidden>暂停动效</button>`)
	// 手动动作在详情页保留，与真实运行观测分开。
	if preview := characterPreview(n.Identity); preview != "" {
		b.WriteString(`<details class="team-preview"><summary>形象预览</summary>` + preview + characterPlayer() + `</details>`)
	}
	teamShell(rw, n.Identity.Name, b.String())
}
func invocationLabel(s string) string {
	switch s {
	case "on_demand":
		return "按需唤起"
	case "polling":
		return "信箱轮询"
	case "":
		return "未声明"
	default:
		return s
	}
}
func member(n *teamNode, id string) bool {
	for _, p := range n.Principals {
		if p == id {
			return true
		}
	}
	return false
}
func sourceError(s string) string {
	return `<p class="team-error">` + esc(s) + `暂时无法读取</p>`
}
func tableRows(rows [][3]string) string {
	var b strings.Builder
	b.WriteString(`<div class="team-table-wrap"><table class="team-table"><thead><tr><th>属性</th><th>值</th><th>来源</th></tr></thead><tbody>`)
	for _, r := range rows {
		fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td>%s</td></tr>`, esc(r[0]), esc(r[1]), esc(r[2]))
	}
	b.WriteString(`</tbody></table></div>`)
	return b.String()
}
func (w *Web) agentMeta(t *teamSnapshot, n *teamNode) string {
	rows := [][3]string{{"display_name", n.Identity.Name, "已发布身份卡"}, {"id", n.ID, teamStatus(n)}, {"principal_ids", strings.Join(n.Principals, ", "), "身份登记"}, {"registry_commit", t.Registry.SHA, "最近已知已发布快照"}}
	if n.Role != nil {
		rows = append(rows, [3]string{"workspace", n.Role.Workspace, "角色声明"}, [3]string{"tags", strings.Join(n.Role.Tags, ", "), "角色声明"}, [3]string{"character", n.Role.Character, "内嵌素材映射"}, [3]string{"invocation", n.Role.Invocation, "配置声明"})
		rows = append(rows, workspaceMeta(n.Role)...)
	}
	rows = append(rows, [3]string{"harness", "按运行实例分别记录", "客户端上报"}, [3]string{"online", "未知", "未接入心跳"}, [3]string{"all_knowledge_fresh", "未知", "不作整体知识同步推断"})
	return `<section class="team-activity"><h2>Agent Meta</h2>` + tableRows(rows) + `</section>`
}

func (w *Web) agentRuns(t *teamSnapshot, r *http.Request, n *teamNode) string {
	var b strings.Builder
	b.WriteString(`<section class="team-activity"><h2>运行信息</h2>`)
	if len(n.Principals) == 0 {
		return b.String() + `<p class="team-empty">尚未接入运行身份</p></section>`
	}
	runs, err := w.DB.ListRuns(n.Principals)
	if err != nil {
		return b.String() + sourceError("运行记录") + `</section>`
	}
	seen, err := w.runSeenAt(t, n)
	if err != nil {
		return b.String() + sourceError("运行历史观测") + `</section>`
	}
	c, err := decodeTeamCursor(r.URL.Query().Get("before"))
	if err != nil {
		return b.String() + `<p class="team-error">无效的分页游标</p></section>`
	}
	type runRow struct {
		run db.Run
		at  time.Time
		key string
	}
	rows := []runRow{}
	for _, run := range runs {
		if !parseTeamTime(run.FirstSeenAt).Before(t.Until) {
			continue
		}
		key := run.PrincipalID + "/" + run.ClientRunID
		// last_seen 会随新请求变化；使用截止时间前的不可变事件重建历史值。
		at := seen[key]
		run.LastSeenAt = ""
		if at.IsZero() {
			at = parseTeamTime(run.FirstSeenAt)
		} else {
			run.LastSeenAt = at.Format(db.TimestampFormat)
		}
		if !at.Before(t.Until) || (!t.Since.IsZero() && at.Before(t.Since)) {
			continue
		}
		if teamBefore(at, key, c) {
			rows = append(rows, runRow{run, at, key})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].at.Equal(rows[j].at) {
			return rows[i].at.After(rows[j].at)
		}
		return rows[i].key > rows[j].key
	})
	q := url.Values{"tab": {"runtime"}}
	if strings.HasPrefix(n.Key, "p:") {
		q.Set("identity", "1")
	}
	b.WriteString(teamRangeForm(r.URL.Path, t, r, q))
	if len(rows) == 0 {
		b.WriteString(`<p class="team-empty">此范围内没有运行观测；不会由旧身份猜测 harness。</p>`)
	}
	more := len(rows) > 100
	if more {
		rows = rows[:100]
	}
	for _, entry := range rows {
		run := entry.run
		last := teamTime(parseTeamTime(run.LastSeenAt))
		if run.LastSeenAt == "" {
			last = "已登记，尚无业务观测"
		}
		device := run.DeviceID
		if device == "" {
			device = "机器未记录"
		}
		fmt.Fprintf(&b, `<article class="team-run" id="run-%s"><h3>%s · %s</h3><small>客户端上报</small>%s</article>`, esc(run.PrincipalID+"/"+run.ClientRunID), esc(device), esc(run.Harness), tableRows([][3]string{{"principal_id", run.PrincipalID, "认证身份"}, {"run_id", run.ClientRunID, "客户端提供，身份内唯一"}, {"workspace", run.Workspace, "客户端上报"}, {"device_id", run.DeviceID, "客户端上报"}, {"first_seen_at", teamTime(parseTeamTime(run.FirstSeenAt)), "服务端登记时间"}, {"last_seen_at", last, "服务端成功业务请求"}}))
	}
	if more {
		last := rows[len(rows)-1]
		q := t.query()
		q.Set("before", encodeTeamCursor(last.at.Format(time.RFC3339Nano), last.key))
		b.WriteString(teamPageNext(r, q, "更早的运行"))
	}
	b.WriteString(`</section>`)
	return b.String()
}

// runSeenAt 从不可变观测和消息重建截止时间内的最近业务请求。
func (w *Web) runSeenAt(t *teamSnapshot, n *teamNode) (map[string]time.Time, error) {
	if t.MailErr != nil {
		return nil, t.MailErr
	}
	out := map[string]time.Time{}
	rows, err := w.DB.Query(`SELECT principal_id,run_id,MAX(occurred_at) FROM agent_observations WHERE run_id IS NOT NULL AND occurred_at<? GROUP BY principal_id,run_id`, t.Until.Format(db.TimestampFormat))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var principal, id, at string
		if err = rows.Scan(&principal, &id, &at); err != nil {
			return nil, err
		}
		if member(n, principal) {
			out[principal+"/"+id] = parseTeamTime(at)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for _, m := range t.AllMessages {
		if m.Runtime == nil || !member(n, m.From) || !m.CreatedAt.Before(t.Until) {
			continue
		}
		key := m.From + "/" + m.Runtime.RunID
		if m.CreatedAt.After(out[key]) {
			out[key] = m.CreatedAt
		}
	}
	return out, nil
}

func (w *Web) recentObservation(t *teamSnapshot, n *teamNode, kind string) (*db.Observation, error) {
	if len(n.Principals) == 0 {
		return nil, nil
	}
	rows, err := w.DB.ListObservations(db.ObservationFilter{Principals: n.Principals, Kind: kind, Since: t.Since, Until: t.Until, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}
func (w *Web) dataSummary(t *teamSnapshot, n *teamNode) string {
	var b strings.Builder
	b.WriteString(`<dl class="team-kv">`)
	b.WriteString(teamKV("共享空间", teamStatus(n)))
	for _, k := range []struct{ kind, label string }{{"file.served", "范围内最近文件返回"}, {"inbox.listed", "范围内最近收信拉取"}} {
		o, err := w.recentObservation(t, n, k.kind)
		value := "未知：未取得记录"
		if err != nil {
			value = "暂时无法读取"
		} else if o != nil {
			value = teamTime(parseTeamTime(o.OccurredAt)) + " · " + o.PrincipalID
			if k.kind == "file.served" {
				value += " · " + o.Resource
			}
		}
		b.WriteString(teamKV(k.label, value))
	}
	b.WriteString(`</dl>`)
	return b.String()
}
func (w *Web) agentData(t *teamSnapshot, n *teamNode) string {
	var b strings.Builder
	b.WriteString(`<section class="team-activity"><h2>数据接入</h2>` + w.dataSummary(t, n))
	if len(n.Principals) > 0 {
		b.WriteString(`<h3>各身份权限</h3>`)
		for _, id := range n.Principals {
			b.WriteString(`<h4>` + esc(id) + `</h4>`)
			access, ok := IdentityAccess{}, false
			if w.AccessForIdentity != nil {
				access, ok = w.AccessForIdentity(id)
			}
			if !ok {
				b.WriteString(`<p class="team-hint">未找到可用的权限配置；不由登记推断授权。</p>`)
				continue
			}
			b.WriteString(tableRows([][3]string{{"read", strings.Join(access.Read, ", "), "当前服务端 key 配置"}, {"owner", strconv.FormatBool(access.Owner), "当前服务端 key 配置"}, {"approve", strconv.FormatBool(access.Approve), "当前服务端 key 配置"}}))
		}
	}
	o, err := w.recentObservation(t, n, "file.served")
	if err != nil {
		b.WriteString(sourceError("文件观测"))
	} else if o != nil {
		b.WriteString(`<h3>最近返回的内容指纹</h3>`)
		b.WriteString(tableRows([][3]string{{"path", o.Resource, "实际返回内容"}, {"content_sha", o.ContentSHA, "实际字节的 Git blob SHA"}}))
		// 只比较服务当前可见字节，不声称远端同步或客户端理解。
		_, current, e := w.Store.ReadWithSHA(o.Resource)
		if e != nil {
			b.WriteString(`<p class="team-hint">当前文件不可读取，无法比较。</p>`)
		} else if current == o.ContentSHA {
			b.WriteString(`<p class="team-hint">与当前服务可见内容相同。</p>`)
		} else {
			b.WriteString(`<p class="team-hint">当前服务可见内容已变化。</p>`)
		}
	}
	if n.Role != nil {
		b.WriteString(`<a class="team-link" href="` + esc(t.nodeURL(n, "workspace")) + `">查看工作空间与使用说明 ↗</a>`)
	}
	b.WriteString(`</section>`)
	return b.String()
}

// contributionActor 只标记提交署名，不将 Git 作者当作已认证请求者。
func contributionActor(t *teamSnapshot, c store.Commit) string {
	candidate := ""
	subject := strings.TrimSpace(c.Subject)
	if strings.HasPrefix(subject, "[") {
		if end := strings.Index(subject, "]"); end > 1 {
			candidate = subject[1:end]
		}
	} else if head, _, ok := strings.Cut(subject, ":"); ok {
		candidate = strings.TrimSpace(head)
	}
	for _, p := range t.Registry.Agents {
		if candidate == p.ID {
			return p.ID
		}
	}
	for _, p := range t.Registry.Agents {
		if c.Author == p.ID {
			return p.ID
		}
	}
	return ""
}
func (w *Web) contributionCounts(t *teamSnapshot) (map[string]int, error) {
	counts := map[string]int{}
	offset := 0
	for {
		p, err := w.Store.History(store.HistoryQuery{SHA: t.Registry.SHA, Since: t.Since, Until: t.Until, Offset: offset, Limit: 100})
		if err != nil {
			return nil, err
		}
		for _, c := range p.Commits {
			if a := contributionActor(t, c); a != "" && a != "owner" {
				counts[t.principalKey(a)]++
			}
		}
		if !p.HasMore {
			return counts, nil
		}
		offset = p.NextOffset
	}
}
func (w *Web) observationCounts(t *teamSnapshot) (map[string]int, error) {
	q := `SELECT principal_id,COUNT(*) FROM agent_observations WHERE occurred_at<?`
	args := []any{t.Until.Format(db.TimestampFormat)}
	if !t.Since.IsZero() {
		q += ` AND occurred_at>=?`
		args = append(args, t.Since.Format(db.TimestampFormat))
	}
	q += ` GROUP BY principal_id`
	rows, err := w.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var id string
		var count int
		if err = rows.Scan(&id, &count); err != nil {
			return nil, err
		}
		if id != "owner" {
			counts[t.principalKey(id)] += count
		}
	}
	return counts, rows.Err()
}
func (w *Web) renderDataMap(t *teamSnapshot, r *http.Request) string {
	contributions, gitErr := w.contributionCounts(t)
	observations, dbErr := w.observationCounts(t)
	var b strings.Builder
	b.WriteString(`<div class="team-map-heading"><span>共享空间</span><span>登记 · 返回观测 · 贡献</span></div>`)
	if gitErr != nil {
		b.WriteString(sourceError("贡献记录"))
	}
	if dbErr != nil {
		b.WriteString(sourceError("观测记录"))
	}
	b.WriteString(`<div class="team-map team-data-map" data-layout="data"><svg class="team-wires" aria-hidden="true">`)
	for i, n := range t.shownNodes() {
		if n.Registered || contributions[n.Key] > 0 || observations[n.Key] > 0 {
			class := ""
			if contributions[n.Key] == 0 && observations[n.Key] == 0 {
				class = "dashed"
				if gitErr != nil || dbErr != nil {
					class = "unknown"
				}
			}
			fmt.Fprintf(&b, `<path class="%s" data-edge="%d" data-from="%s" data-to="shared"/>`, class, i, esc(n.Key))
		}
	}
	b.WriteString(`</svg><div class="team-source" data-node="shared"><strong>共享空间</strong><small>Git 资料 + SQLite 状态</small></div>`)
	for i, n := range t.shownNodes() {
		q := t.query()
		q.Set("view", "data")
		q.Set("node", n.Key)
		label := teamStatus(n)
		if n.Registered {
			if observations[n.Key] > 0 || contributions[n.Key] > 0 {
				label = "有交互记录"
			} else if gitErr != nil || dbErr != nil {
				label = "交互未知"
			}
		}
		fmt.Fprintf(&b, `<a class="team-node" data-node="%s" href="%s#team-detail">%s<strong>%s</strong><span class="team-tag">%s</span></a>`, esc(n.Key), esc(teamURL("/d/agents", q)), teamAvatar(n.Identity), esc(n.Identity.Name), esc(label))
		if !n.Registered && contributions[n.Key] == 0 && observations[n.Key] == 0 {
			continue
		}
		edgeLabel := "已登记"
		if gitErr != nil || dbErr != nil {
			edgeLabel = "交互未知"
		}
		if contributions[n.Key] > 0 || observations[n.Key] > 0 {
			parts := []string{}
			if observations[n.Key] > 0 {
				parts = append(parts, fmt.Sprintf("观测 %d", observations[n.Key]))
			}
			if contributions[n.Key] > 0 {
				parts = append(parts, fmt.Sprintf("署名提交 %d", contributions[n.Key]))
			}
			if gitErr != nil {
				parts = append(parts, "贡献不可用")
			}
			if dbErr != nil {
				parts = append(parts, "观测不可用")
			}
			edgeLabel = strings.Join(parts, " · ")
		}
		fmt.Fprintf(&b, `<a class="team-edge-label" data-edge="%d" href="%s">%s</a>`, i, esc(t.nodeURL(n, "data")), esc(edgeLabel))
	}
	b.WriteString(`</div><div class="team-legend">实线：有记录　虚线：登记关系　点线：交互未知</div>`)
	return b.String()
}

func (w *Web) renderTeamActivity(t *teamSnapshot, r *http.Request, n *teamNode) string {
	typ := r.URL.Query().Get("type")
	if typ == "" {
		typ = "mail"
	}
	valid := false
	types := []struct{ id, name string }{{"mail", "信箱"}, {"contributions", "资料贡献"}, {"proposals", "当前提案"}, {"reads", "文件返回"}, {"polls", "收信拉取"}, {"decisions", "审批记录"}, {"execution", "外部执行"}}
	for _, v := range types {
		if typ == v.id {
			valid = true
		}
	}
	if !valid {
		return `<p class="team-error">无效的活动类型</p>`
	}
	var b strings.Builder
	b.WriteString(`<section class="team-activity"><div class="team-activity-heading"><h2>最近活动</h2></div>`)
	fields := t.query()
	if n == nil {
		fields.Set("view", "activity")
	} else {
		fields.Set("tab", "activity")
		if strings.HasPrefix(n.Key, "p:") {
			fields.Set("identity", "1")
		}
	}
	if p := r.URL.Query().Get("pair"); p != "" {
		fields.Set("pair", p)
	}
	fmt.Fprintf(&b, `<form method="get" action="%s" class="team-activity-filter">`, esc(r.URL.Path))
	for k, vs := range fields {
		for _, v := range vs {
			fmt.Fprintf(&b, `<input type="hidden" name="%s" value="%s">`, esc(k), esc(v))
		}
	}
	b.WriteString(`<label>类型 <select name="type" aria-label="活动类型">`)
	for _, v := range types {
		sel := ""
		if typ == v.id {
			sel = " selected"
		}
		fmt.Fprintf(&b, `<option value="%s"%s>%s</option>`, v.id, sel, v.name)
	}
	b.WriteString(`</select></label>`)
	if typ == "mail" {
		b.WriteString(`<label>范围 <select name="scope" aria-label="往来范围">`)
		for _, v := range []struct{ id, name string }{{"agents", "Agent 间"}, {"all", "包含本人"}} {
			sel := ""
			if (r.URL.Query().Get("scope") == "all") == (v.id == "all") {
				sel = " selected"
			}
			fmt.Fprintf(&b, `<option value="%s"%s>%s</option>`, v.id, sel, v.name)
		}
		b.WriteString(`</select></label>`)
		if n != nil {
			b.WriteString(`<label>方向 <select name="direction" aria-label="消息方向">`)
			for _, v := range []struct{ id, name string }{{"all", "全部往来"}, {"sent", "发出"}, {"received", "收到"}} {
				sel := ""
				if r.URL.Query().Get("direction") == v.id {
					sel = " selected"
				}
				fmt.Fprintf(&b, `<option value="%s"%s>%s</option>`, v.id, sel, v.name)
			}
			b.WriteString(`</select></label>`)
		}
	}
	b.WriteString(`<button class="team-text" type="submit">筛选</button></form>`)
	if n != nil {
		extra := cloneQuery(fields)
		extra.Del("range")
		extra.Del("at")
		extra.Set("type", typ)
		b.WriteString(teamRangeForm(r.URL.Path, t, r, extra))
	}
	switch typ {
	case "mail":
		b.WriteString(w.messageActivity(t, r, n))
	case "reads", "polls":
		kind := "file.served"
		if typ == "polls" {
			kind = "inbox.listed"
		}
		b.WriteString(w.observationActivity(t, r, n, kind))
	case "contributions":
		b.WriteString(w.commitActivity(t, r, n))
	case "proposals":
		b.WriteString(w.proposalActivity(t, n))
	case "decisions":
		b.WriteString(w.decisionActivity(t, r, n))
	case "execution":
		b.WriteString(`<p class="team-empty">尚未接入外部执行记录</p>`)
	}
	b.WriteString(`</section>`)
	return b.String()
}
func (w *Web) messageActivity(t *teamSnapshot, r *http.Request, n *teamNode) string {
	if t.MailErr != nil {
		return sourceError("信箱")
	}
	c, err := decodeTeamCursor(r.URL.Query().Get("before"))
	if err != nil {
		return `<p class="team-error">无效的分页游标</p>`
	}
	rows := []protocol.Message{}
	includeOwner := r.URL.Query().Get("scope") == "all"
	direction := r.URL.Query().Get("direction")
	for _, m := range t.Messages {
		if !includeOwner && (m.From == "owner" || m.To == "owner") {
			continue
		}
		from, to := t.senderKey(m), t.principalKey(m.To)
		if n != nil {
			if from != n.Key && to != n.Key {
				continue
			}
			if direction == "sent" && from != n.Key {
				continue
			}
			if direction == "received" && to != n.Key {
				continue
			}
		}
		if p := r.URL.Query().Get("pair"); p != "" {
			a, b := from, to
			if a > b {
				a, b = b, a
			}
			if a+","+b != p {
				continue
			}
		}
		if teamBefore(m.CreatedAt, m.ID, c) {
			rows = append(rows, m)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].CreatedAt.After(rows[j].CreatedAt)
		}
		return rows[i].ID > rows[j].ID
	})
	more := len(rows) > 100
	if more {
		rows = rows[:100]
	}
	if len(rows) == 0 {
		return `<p class="team-empty">此范围内未观察到往来</p>`
	}
	var b strings.Builder
	for _, m := range rows {
		badge := "发起新话题"
		if m.ReplyTo != "" {
			badge = "发送回复"
		}
		runtime := "harness 未记录"
		if m.Runtime != nil {
			device := m.Runtime.DeviceID
			if device == "" {
				device = "机器未记录"
			}
			runtime = device + " · " + m.Runtime.Harness + " · run " + m.Runtime.RunID + " · " + m.Runtime.Workspace + " · 客户端上报"
		}
		fmt.Fprintf(&b, `<article class="team-event"><time>%s</time><div><a href="%s"><strong>%s ↗</strong></a><small>%s → %s · %s</small><details><summary>来源</summary><p>%s → %s · %s</p></details></div><span class="team-tag">%s</span></article>`, esc(teamTime(m.CreatedAt)), esc(messageURL(m)), esc(conversationTitle(m.Task)), esc(t.label(t.senderKey(m))), esc(t.label(t.principalKey(m.To))), esc(m.Item), esc(m.From), esc(m.To), esc(runtime), badge)
	}
	if more {
		m := rows[len(rows)-1]
		q := t.query()
		q.Set("before", encodeTeamCursor(m.CreatedAt.Format(time.RFC3339Nano), m.ID))
		b.WriteString(teamPageNext(r, q, "更早的消息"))
	}
	return b.String()
}
func (w *Web) observationActivity(t *teamSnapshot, r *http.Request, n *teamNode, kind string) string {
	if n != nil && len(n.Principals) == 0 {
		return `<p class="team-empty">尚未接入，观测未知</p>`
	}
	c, err := decodeTeamCursor(r.URL.Query().Get("before"))
	if err != nil {
		return `<p class="team-error">无效的分页游标</p>`
	}
	beforeID := int64(0)
	if c.ID != "" {
		beforeID, err = strconv.ParseInt(c.ID, 10, 64)
		if err != nil {
			return `<p class="team-error">无效的分页游标</p>`
		}
	}
	f := db.ObservationFilter{Kind: kind, Since: t.Since, Until: t.Until, BeforeAt: c.At, BeforeID: beforeID, Limit: 100}
	if n != nil {
		f.Principals = n.Principals
	}
	rows, err := w.DB.ListObservations(f)
	if err != nil {
		return sourceError("观测")
	}
	if len(rows) == 0 {
		return `<p class="team-empty">此范围内未取得观测记录</p>`
	}
	var b strings.Builder
	for _, o := range rows {
		title := "返回文件：" + o.Resource
		href := "/d/file?path=" + url.QueryEscape(o.Resource)
		if kind == "inbox.listed" {
			title = "返回信箱列表"
			href = "/d/groups"
		}
		detailRows := [][3]string{{"run_id", o.RunID, "可选运行引用"}, {"content_sha", o.ContentSHA, "返回内容指纹"}, {"metadata", o.MetadataJSON, "请求筛选语义"}}
		if o.RunID != "" {
			run, err := w.DB.GetRun(o.PrincipalID, o.RunID)
			if err == nil {
				detailRows = append(detailRows, [3]string{"device_id", run.DeviceID, "关联运行声明"}, [3]string{"harness", run.Harness, "关联运行声明"}, [3]string{"workspace", run.Workspace, "关联运行声明"})
			} else {
				detailRows = append(detailRows, [3]string{"runtime", "暂时无法读取", "关联运行"})
			}
		}
		fmt.Fprintf(&b, `<article class="team-event"><time>%s</time><div><a href="%s"><strong>%s ↗</strong></a><small>%s · %s</small><details><summary>观测详情</summary>%s</details></div><span class="team-tag">服务端记录</span></article>`, esc(teamTime(parseTeamTime(o.OccurredAt))), esc(href), esc(title), esc(t.label(t.principalKey(o.PrincipalID))), esc(o.PrincipalID), tableRows(detailRows))
	}
	if len(rows) == 100 {
		last := rows[len(rows)-1]
		q := t.query()
		q.Set("before", encodeTeamCursor(last.OccurredAt, strconv.FormatInt(last.ID, 10)))
		b.WriteString(teamPageNext(r, q, "更早的观测"))
	}
	return b.String()
}
func (w *Web) commitActivity(t *teamSnapshot, r *http.Request, n *teamNode) string {
	offset := 0
	var err error
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return `<p class="team-error">无效的历史游标</p>`
		}
	}
	sha := r.URL.Query().Get("history_sha")
	if sha == "" {
		sha = t.Registry.SHA
	}
	p, err := w.Store.History(store.HistoryQuery{SHA: sha, Since: t.Since, Until: t.Until, Offset: offset, Limit: 100})
	if err != nil {
		return sourceError("Git 历史")
	}
	var b strings.Builder
	count := 0
	for _, c := range p.Commits {
		actor := contributionActor(t, c)
		if n != nil && (actor == "" || t.principalKey(actor) != n.Key) {
			continue
		}
		count++
		label := "未归属署名：" + c.Author
		if actor != "" {
			label = t.label(t.principalKey(actor)) + " · " + actor
		}
		fmt.Fprintf(&b, `<article class="team-event"><time>%s</time><div><strong>%s</strong><small>%s · %s</small></div><span class="team-tag">已合入 · 署名</span></article>`, esc(teamTime(c.At)), esc(c.Subject), esc(label), esc(c.SHA))
	}
	if count == 0 {
		b.WriteString(`<p class="team-empty">本页没有符合条件的已合入贡献</p>`)
	}
	if p.HasMore {
		q := t.query()
		q.Set("history_sha", p.SHA)
		q.Set("offset", strconv.Itoa(p.NextOffset))
		b.WriteString(teamPageNext(r, q, "继续查看历史"))
	}
	b.WriteString(`<p class="team-hint">按 Git 署名归属；固定发布快照分页，不表示每次 push 都已合入。</p>`)
	return b.String()
}
func (w *Web) decisionActivity(t *teamSnapshot, r *http.Request, n *teamNode) string {
	rows, err := w.DB.Query(`SELECT name,agent,status,decided_at,decided_by FROM branch_decisions ORDER BY decided_at DESC,name DESC`)
	if err != nil {
		return sourceError("审批记录")
	}
	defer rows.Close()
	c, err := decodeTeamCursor(r.URL.Query().Get("before"))
	if err != nil {
		return `<p class="team-error">无效的分页游标</p>`
	}
	var b strings.Builder
	count := 0
	more := false
	var lastAt time.Time
	lastID := ""
	for rows.Next() {
		var name, agent, status, at, by string
		if e := rows.Scan(&name, &agent, &status, &at, &by); e != nil {
			return sourceError("审批记录")
		}
		date := parseTeamTime(at)
		if date.IsZero() || !date.Before(t.Until) || (!t.Since.IsZero() && date.Before(t.Since)) {
			continue
		}
		if n != nil && t.principalKey(agent) != n.Key && t.principalKey(by) != n.Key {
			continue
		}
		if !teamBefore(date, name, c) {
			continue
		}
		if count == 100 {
			more = true
			break
		}
		count++
		lastAt, lastID = date, name
		label := map[string]string{"applied": "已合入", "rejected": "已拒绝", "conflict": "冲突"}[status]
		if label == "" {
			label = status
		}
		fmt.Fprintf(&b, `<article class="team-event"><time>%s</time><div><strong>%s</strong><small>提交：%s · 决定：%s</small></div><span class="team-tag">%s</span></article>`, esc(teamTime(date)), esc(name), esc(agent), esc(by), esc(label))
	}
	if rows.Err() != nil {
		return sourceError("审批记录")
	}
	if count == 0 {
		b.WriteString(`<p class="team-empty">此范围内没有审批记录</p>`)
	}
	if more {
		q := t.query()
		q.Set("before", encodeTeamCursor(lastAt.Format(time.RFC3339Nano), lastID))
		b.WriteString(teamPageNext(r, q, "更早的审批"))
	}
	return b.String()
}

func (w *Web) proposalActivity(t *teamSnapshot, n *teamNode) string {
	branches, err := w.Store.ListBranches()
	if err != nil {
		return sourceError("当前提案")
	}
	var b strings.Builder
	b.WriteString(`<p class="team-hint">当前服务已知的待确认分支，不按历史时间范围筛选。</p>`)
	count := 0
	for _, branch := range branches {
		if n != nil && t.principalKey(branch.Agent) != n.Key {
			continue
		}
		var state string
		err = w.DB.QueryRow(`SELECT status FROM branch_decisions WHERE name=?`, branch.Name).Scan(&state)
		if err != nil && err != sql.ErrNoRows {
			return sourceError("提案决定")
		}
		if state == "rejected" {
			continue
		}
		count++
		fmt.Fprintf(&b, `<article class="team-event"><time>%s</time><div><a href="/d/branches"><strong>%s ↗</strong></a><small>%s · %s</small></div><span class="team-tag">待确认分支</span></article>`, esc(branch.Date), esc(branch.Subject), esc(branch.Name), esc(branch.Agent))
	}
	if count == 0 {
		b.WriteString(`<p class="team-empty">没有当前待确认提案</p>`)
	}
	return b.String()
}

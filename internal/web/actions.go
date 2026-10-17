package web

import (
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ketchupz1999/agents-home-public/internal/agents"
	"github.com/ketchupz1999/agents-home-public/internal/db"
	"github.com/ketchupz1999/agents-home-public/internal/model"
)

//go:embed actions.css
var actionsCSS string

func actionDates(now time.Time) (today, week, end string) {
	t := now.In(teamZone)
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, teamZone)
	monday := d.AddDate(0, 0, -(int(d.Weekday())+6)%7)
	return d.Format("2006-01-02"), monday.Format("2006-01-02"), monday.AddDate(0, 0, 6).Format("2006-01-02")
}

func actionBucket(a db.Action, now time.Time) string {
	if a.Status != "planned" {
		return a.Status
	}
	today, week, end := actionDates(now)
	if a.PlanKind == "later" {
		return "later"
	}
	if a.PlanKind == "week" {
		if a.PlanDate < week {
			return "overdue"
		}
		if a.PlanDate == week {
			return "week"
		}
		return "future"
	}
	if a.PlanDate < today {
		return "overdue"
	}
	if a.PlanDate == today {
		return "today"
	}
	if a.PlanDate <= end {
		return "week"
	}
	return "future"
}

func actionPlan(a db.Action) string {
	switch a.PlanKind {
	case "day":
		return a.PlanDate
	case "week":
		return a.PlanDate + " 这一周"
	default:
		return "未安排日期"
	}
}

func actionState(s string) string {
	switch s {
	case "suggested":
		return "待采纳"
	case "planned":
		return "已安排"
	case "done":
		return "已完成"
	case "dismissed":
		return "已略过"
	default:
		return s
	}
}

func (w *Web) actionCSRF() string {
	m := hmac.New(sha256.New, w.SecretKey)
	m.Write([]byte("dashboard|owner|actions"))
	return hex.EncodeToString(m.Sum(nil))
}

// actionBoard 只渲染持久记录，不把来源文字或 Demo 转成已确认行动。
func (w *Web) actionBoard(g *model.Graph) string {
	var b strings.Builder
	b.WriteString(`<style>` + actionsCSS + `</style><section class="action-board" id="today" aria-label="行动清单">`)
	actions, err := w.DB.ListActions("", "")
	if err != nil {
		return b.String() + `<p class="overview-error" role="alert">行动清单暂时无法读取，请刷新重试。</p></section>`
	}
	now := w.teamNow()
	today, week, end := actionDates(now)
	groups := map[string][]db.Action{}
	for _, a := range actions {
		key := actionBucket(a, now)
		groups[key] = append(groups[key], a)
	}
	for _, key := range []string{"week", "overdue", "future"} {
		sort.SliceStable(groups[key], func(i, j int) bool { return groups[key][i].PlanDate < groups[key][j].PlanDate })
	}
	for _, key := range []string{"done", "dismissed"} {
		sort.SliceStable(groups[key], func(i, j int) bool { return groups[key][i].UpdatedAt > groups[key][j].UpdatedAt })
	}
	reg, _ := agents.Load(w.Store)
	b.WriteString(`<div class="action-grid"><div><div class="section-head"><h2>今天 <span class="section-count">` + strconv.Itoa(len(groups["today"])) + `</span></h2><span class="action-date">` + today + `</span></div>`)
	if len(groups["today"]) == 0 {
		b.WriteString(`<div class="action-empty">今天还没有安排。可以从建议或本周挑一件。</div>`)
	}
	for _, a := range groups["today"] {
		b.WriteString(w.actionCard(a, g, reg, "today"))
	}
	b.WriteString(`<section class="action-week" id="week"><div class="section-head"><h2>本周 <span class="section-count">` + strconv.Itoa(len(groups["week"])) + `</span></h2><span class="action-date">` + week + ` — ` + end + `</span></div>`)
	if len(groups["week"]) == 0 {
		b.WriteString(`<p class="action-empty-line">本周暂无其他安排。</p>`)
	}
	for _, a := range groups["week"] {
		b.WriteString(w.actionCard(a, g, reg, "week"))
	}
	b.WriteString(`</section>`)
	if len(groups["overdue"]) > 0 {
		b.WriteString(`<section class="action-overdue"><div class="section-head"><h2>待重新安排 <span class="section-count">` + strconv.Itoa(len(groups["overdue"])) + `</span></h2></div>`)
		for _, a := range groups["overdue"] {
			b.WriteString(w.actionCard(a, g, reg, "overdue"))
		}
		b.WriteString(`</section>`)
	}
	b.WriteString(`</div><aside><div class="section-head"><h2>小队的建议 <span class="section-count">` + strconv.Itoa(len(groups["suggested"])) + `</span></h2></div>`)
	if len(groups["suggested"]) == 0 {
		b.WriteString(`<div class="action-suggestion-empty">暂时没有新建议。</div>`)
	}
	for _, a := range groups["suggested"] {
		b.WriteString(w.actionCard(a, g, reg, "suggested"))
	}
	b.WriteString(`</aside></div>`)
	for _, section := range []struct{ key, title string }{{"later", "改天再做"}, {"future", "以后安排"}, {"done", "已完成"}, {"dismissed", "已略过"}} {
		list := groups[section.key]
		fmt.Fprintf(&b, `<details class="action-archive" id="actions-%s"><summary>%s <span class="section-count">%d</span></summary>`, section.key, section.title, len(list))
		if len(list) == 0 {
			b.WriteString(`<p class="action-empty-line">暂时没有。</p>`)
		}
		for _, a := range list {
			b.WriteString(w.actionCard(a, g, reg, section.key))
		}
		b.WriteString(`</details>`)
	}
	b.WriteString(`</section>`)
	return b.String()
}

func (w *Web) actionButton(a db.Action, op, when, label string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<form method="post" action="/d/actions/%s/%s"><input type="hidden" name="csrf" value="%s"><input type="hidden" name="revision" value="%d">`, url.PathEscape(a.ID), op, w.actionCSRF(), a.Revision)
	if when != "" {
		fmt.Fprintf(&b, `<input type="hidden" name="when" value="%s">`, when)
	}
	class := "action-button"
	if op == "complete" || (a.Status == "suggested" && when == "today") {
		class += " action-primary"
	}
	fmt.Fprintf(&b, `<button class="%s" type="submit">%s</button></form>`, class, esc(label))
	return b.String()
}

func (w *Web) actionCard(a db.Action, g *model.Graph, reg *agents.Registry, bucket string) string {
	var b strings.Builder
	creator := defaultIdentity(a.CreatedBy)
	if reg != nil {
		creator = w.identityAt(reg, a.CreatedBy, a.CreatedRole)
	} else if a.CreatedRole != "" {
		creator = defaultIdentity(a.CreatedRole)
	}
	item := a.Item
	if n := g.Get("item:" + a.Item); n != nil {
		item = n.Title
	}
	fmt.Fprintf(&b, `<article class="action-card action-%s" data-action-id="%s"><div class="action-card-top"><a href="/d/item/%s">%s ↗</a><span>%s</span></div>`, bucket, esc(a.ID), url.PathEscape(a.Item), esc(item), esc(actionPlan(a)))
	if a.Status == "suggested" {
		fmt.Fprintf(&b, `<div class="action-author">%s<span>%s<small>提出建议</small></span></div>`, avatar(creator), esc(creator.Name))
	}
	fmt.Fprintf(&b, `<h3>%s</h3>`, esc(a.Title))
	if a.Details != "" {
		b.WriteString(`<p class="action-description">` + esc(a.Details) + `</p>`)
	}
	b.WriteString(`<div class="action-controls">`)
	switch a.Status {
	case "suggested":
		b.WriteString(w.actionButton(a, "schedule", "today", "放到今天") + w.actionButton(a, "schedule", "week", "放到本周") + w.actionButton(a, "schedule", "later", "改天再做") + w.actionButton(a, "dismiss", "", "暂不采纳"))
	case "planned":
		if bucket != "today" {
			b.WriteString(w.actionButton(a, "schedule", "today", "放到今天"))
		}
		b.WriteString(w.actionButton(a, "complete", "", "✓ 完成"))
	case "done":
		b.WriteString(w.actionButton(a, "reopen", "", "撤销完成"))
	}
	b.WriteString(`</div>`)
	if a.Status == "planned" {
		b.WriteString(`<details class="action-reschedule"><summary>改期</summary><div class="action-controls">` + w.actionButton(a, "schedule", "tomorrow", "明天") + w.actionButton(a, "schedule", "week", "本周稍后") + w.actionButton(a, "schedule", "later", "先不安排日期") + `</div>`)
		fmt.Fprintf(&b, `<form class="action-date-form" method="post" action="/d/actions/%s/schedule"><input type="hidden" name="csrf" value="%s"><input type="hidden" name="revision" value="%d"><input type="hidden" name="when" value="date"><label>选择日期<input type="date" name="date" value="%s" required></label><button type="submit" class="action-button">保存日期</button></form></details>`, url.PathEscape(a.ID), w.actionCSRF(), a.Revision, esc(a.PlanDate))
	}
	b.WriteString(`<details class="action-evidence"><summary>来源与记录</summary>`)
	fmt.Fprintf(&b, `<p>由 %s 提出 · %s</p>`, esc(creator.Name), esc(teamTime(parseTeamTime(a.CreatedAt))))
	if a.SourceMessageID != "" {
		if m, err := w.DB.GetMessage(a.SourceMessageID); err == nil && m != nil && m.GroupID != "" {
			fmt.Fprintf(&b, `<a href="%s">查看来源群组 ↗</a>`, esc(messageURL(*m)))
		}
	}
	if a.SourcePath != "" {
		fmt.Fprintf(&b, `<a href="/d/file?path=%s">%s ↗</a><small>提出时内容指纹：%s</small>`, esc(url.QueryEscape(a.SourcePath)), esc(a.SourcePath), esc(a.SourceSHA))
	}
	if a.Runtime != nil {
		fmt.Fprintf(&b, `<p>运行声明：%s · %s · %s</p>`, esc(a.Runtime.DeviceID), esc(a.Runtime.Harness), esc(a.Runtime.RunID))
	}
	history, err := w.DB.ActionHistory(a.ID)
	if err != nil {
		b.WriteString(`<p class="overview-error">变更记录暂时无法读取。</p>`)
	} else {
		b.WriteString(`<ol class="action-history">`)
		for _, h := range history {
			fmt.Fprintf(&b, `<li><time>%s</time> %s · %s · %s</li>`, esc(teamTime(parseTeamTime(h.At))), esc(h.Actor), esc(actionState(h.Action.Status)), esc(actionPlan(h.Action)))
		}
		b.WriteString(`</ol>`)
	}
	b.WriteString(`</details></article>`)
	return b.String()
}

// saveAction 只接受已登录 owner 的表单，采纳或完成不会触发外部执行。
func (w *Web) saveAction(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(rw, r.Body, 8192)
	if err := r.ParseForm(); err != nil {
		http.Error(rw, "表单无法读取", 400)
		return
	}
	if len(r.PostForm["csrf"]) != 1 || !hmac.Equal([]byte(r.PostForm.Get("csrf")), []byte(w.actionCSRF())) {
		http.Error(rw, "页面校验失败，请刷新后重试", 403)
		return
	}
	revision, err := strconv.ParseInt(r.PostForm.Get("revision"), 10, 64)
	if err != nil || revision < 1 || len(r.PostForm["revision"]) != 1 {
		http.Error(rw, "行动版本无效", 400)
		return
	}
	u := db.ActionUpdate{Operation: r.PathValue("operation"), Revision: revision}
	if u.Operation == "schedule" {
		if len(r.PostForm["when"]) != 1 {
			http.Error(rw, "请选择安排时间", 400)
			return
		}
		today, week, _ := actionDates(w.teamNow())
		switch r.PostForm.Get("when") {
		case "today":
			u.PlanKind, u.PlanDate = "day", today
		case "tomorrow":
			u.PlanKind, u.PlanDate = "day", w.teamNow().In(teamZone).AddDate(0, 0, 1).Format("2006-01-02")
		case "week":
			u.PlanKind, u.PlanDate = "week", week
		case "later":
			u.PlanKind = "later"
		case "date":
			if len(r.PostForm["date"]) != 1 {
				http.Error(rw, "请选择日期", 400)
				return
			}
			u.PlanKind, u.PlanDate = "day", r.PostForm.Get("date")
		default:
			http.Error(rw, "无效的安排时间", 400)
			return
		}
	}
	_, err = w.DB.UpdateAction(r.PathValue("id"), db.ActionActor{Name: w.owner(), Owner: true}, u)
	if err == nil {
		http.Redirect(rw, r, "/d/#today", http.StatusSeeOther)
		return
	}
	status, message := 503, "保存结果未确认，请刷新核对后再操作。"
	switch {
	case errors.Is(err, db.ErrActionNotFound):
		status, message = 404, "行动不存在。"
	case errors.Is(err, db.ErrActionConflict):
		status, message = 409, "这个行动已被其他页面更新，请刷新核对后再操作。"
	case errors.Is(err, db.ErrActionValidation):
		status, message = 400, "当前状态或安排日期无效，请刷新核对后重试。"
	case errors.Is(err, db.ErrActionForbidden):
		status, message = 403, "只有本人可以调整安排。"
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.WriteHeader(status)
	page(rw, "行动未保存", "/d/", `<section class="card"><h1>行动未保存</h1><p role="alert">`+esc(message)+`</p><a href="/d/#today">返回行动清单 ↗</a></section>`)
}

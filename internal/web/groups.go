package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ketchupz1999/agents-home-public/internal/agents"
)

// groups renders a list of groups the owner belongs to.
func (w *Web) groups(rw http.ResponseWriter, r *http.Request) {
	item := r.URL.Query().Get("item")
	statusFilter := "active"
	if r.URL.Query().Get("status") == "all" {
		statusFilter = "all"
	}
	// Owner sees all groups, not just their own.
	groups, err := w.DB.ListGroups("", item, statusFilter)
	if err != nil {
		page(rw, "群组", "/d/groups", `<div class="card error">读取群组失败</div>`)
		return
	}

	reg, _ := agents.Load(w.Store)

	var b strings.Builder
	newURL := "/d/groups/new"
	if item != "" {
		newURL += "?item=" + url.QueryEscape(item)
	}
	newButton := ""
	if w.CreateGroup != nil {
		newButton = fmt.Sprintf(`<a class="gf-new" href="%s">＋ 新建群组</a>`, esc(newURL))
	}
	fmt.Fprintf(&b, `<header class="page-heading"><div><p class="eyebrow">多人协作讨论</p><h1>群组</h1></div><span class="muted">%d 个群组</span>%s</header>`, len(groups), newButton)

	// Filters
	base := "/d/groups"
	if item != "" {
		base += "?item=" + url.QueryEscape(item)
	}
	allURL := base
	var allStatusURL string
	if item != "" {
		allStatusURL = base + "&status=all"
	} else {
		allStatusURL = base + "?status=all"
	}
	activeClass, allClass := "selected", ""
	if statusFilter == "all" {
		activeClass, allClass = "", "selected"
	}
	fmt.Fprintf(&b, `<div class="filters"><a class="%s" href="%s">进行中</a><a class="%s" href="%s">全部</a></div>`,
		esc(activeClass), esc(allURL), esc(allClass), esc(allStatusURL))

	if item != "" {
		fmt.Fprintf(&b, `<p class="muted">筛选事项：%s · <a href="/d/groups">清除筛选</a></p>`, esc(item))
	}

	if len(groups) == 0 {
		b.WriteString(`<div class="card empty"><h2>暂无群组</h2><p class="muted">群组讨论创建后会出现在这里。</p></div>`)
		page(rw, "群组", "/d/groups", b.String())
		return
	}

	b.WriteString(`<div class="conversation-list">`)
	for _, g := range groups {
		msgCount := w.DB.CountGroupMessages(g.ID)

		cls := "conversation-row"
		if g.Status == "closed" {
			cls += " group-closed"
		}
		fmt.Fprintf(&b, `<a class="%s" href="/d/groups/%s">`, esc(cls), esc(url.PathEscape(g.ID)))

		// Member avatar stack
		b.WriteString(`<div class="avatar-stack">`)
		for i, member := range g.Members {
			if i >= 3 {
				break
			}
			identity := w.identityAt(reg, member, "")
			b.WriteString(avatar(identity))
		}
		if len(g.Members) > 3 {
			fmt.Fprintf(&b, `<span class="avatar avatar-more">+%d</span>`, len(g.Members)-3)
		}
		b.WriteString(`</div>`)

		b.WriteString(`<div class="conversation-main"><div class="conversation-top">`)
		fmt.Fprintf(&b, `<h2>%s</h2>`, esc(g.Topic))
		fmt.Fprintf(&b, `<time>%s</time></div>`, esc(truncateTime(g.CreatedAt)))

		// Members line
		var names []string
		for _, member := range g.Members {
			identity := w.identityAt(reg, member, "")
			names = append(names, identityLabel(identity))
		}
		fmt.Fprintf(&b, `<p class="conversation-people">%s</p>`, esc(strings.Join(names, " · ")))

		// Bottom row: item, member count, message count, status badge
		b.WriteString(`<div class="conversation-bottom">`)
		if g.Item != "" {
			fmt.Fprintf(&b, `<span class="conversation-item">%s</span>`, esc(g.Item))
		}
		fmt.Fprintf(&b, `<span>%d 位成员</span><span>%d 条消息</span>`, len(g.Members), msgCount)
		if g.Status == "closed" {
			b.WriteString(`<span class="group-status-badge group-status-closed">已关闭</span>`)
		} else {
			b.WriteString(`<span class="group-status-badge group-status-active">进行中</span>`)
		}
		b.WriteString(`</div></div><span class="conversation-arrow" aria-hidden="true">›</span></a>`)
	}
	b.WriteString(`</div>`)

	b.WriteString(`<style>` + groupCSS + `</style>`)
	page(rw, "群组", "/d/groups", b.String())
}

// groupDetail renders a single group's messages like a chat thread.
func (w *Web) groupDetail(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	g, err := w.DB.GetGroup(id)
	if err != nil || g == nil {
		page(rw, "群组", "/d/groups", `<div class="card error">群组不存在</div>`)
		return
	}

	messages, err := w.DB.ListGroupMessages(g.ID, 200)
	if err != nil {
		page(rw, "群组", "/d/groups", `<div class="card error">读取消息失败</div>`)
		return
	}

	reg, identityErr := agents.Load(w.Store)

	var b strings.Builder
	if identityErr != nil {
		b.WriteString(sourceError("角色资料"))
	}

	b.WriteString(`<div class="thread-page">`)
	b.WriteString(`<a class="back" href="/d/groups">← 返回群组列表</a>`)
	fmt.Fprintf(&b, `<header class="thread-heading"><div class="thread-heading-top"><p class="eyebrow">群组讨论</p></div><h1>%s</h1>`, esc(g.Topic))

	// Context line: item, status, message count
	b.WriteString(`<div class="thread-context">`)
	if g.Item != "" {
		fmt.Fprintf(&b, `<a href="/d/item/%s">%s ↗</a>`, esc(url.PathEscape(g.Item)), esc(g.Item))
	}
	statusLabel := "进行中"
	if g.Status == "closed" {
		statusLabel = "已关闭"
	}
	fmt.Fprintf(&b, `<span>%s</span><span>%d 条消息 · %d 位成员</span></div>`, esc(statusLabel), len(messages), len(g.Members))

	// Participants
	b.WriteString(`<div class="thread-participants">`)
	for _, member := range g.Members {
		identity := w.identityAt(reg, member, "")
		fmt.Fprintf(&b, `<span class="thread-team-member">%s%s</span>`, avatar(identity), identityText(identity))
	}
	b.WriteString(`</div></header>`)

	// Messages
	b.WriteString(`<div class="chat-history">`)
	day := ""
	unreadCount := 0
	for _, m := range messages {
		if !w.DB.IsRead(w.owner(), m.ID) {
			unreadCount++
		}
		date := m.CreatedAt.Local().Format("2006-01-02")
		if date != day {
			fmt.Fprintf(&b, `<div class="chat-day"><span>%s</span></div>`, esc(date))
			day = date
		}

		cls := "relay"
		if m.From == w.owner() {
			cls = "outgoing"
		} else if m.To == w.owner() || containsMention(m.Mentions, w.owner()) {
			cls = "incoming"
		}

		senderIdentity := w.identityAt(reg, m.From, m.FromRole)
		fmt.Fprintf(&b, `<article class="chat-message %s" id="msg-%s">%s<div class="chat-content"><div class="chat-byline">%s`,
			esc(cls), esc(m.ID), avatar(senderIdentity), identityText(senderIdentity))

		// Show mentions
		if len(m.Mentions) > 0 {
			var mentionNames []string
			for _, mention := range m.Mentions {
				mi := w.identityAt(reg, mention, "")
				mentionNames = append(mentionNames, "@"+mi.Name)
			}
			fmt.Fprintf(&b, `<span class="chat-recipient">→ %s</span>`, esc(strings.Join(mentionNames, " ")))
		}

		fmt.Fprintf(&b, `<time>%s</time></div>`, esc(m.CreatedAt.Local().Format("15:04")))

		// Bubble
		b.WriteString(`<div class="chat-bubble">`)
		fmt.Fprintf(&b, `<div class="chat-kind">%s</div>`, esc(messageKind(m.Kind)))

		task := strings.TrimSpace(m.Task)
		if strings.TrimSpace(m.Body) == "" && task != "" {
			b.WriteString(`<div class="markdown"><p>` + esc(conversationTitle(task)) + `</p></div>`)
		} else if strings.TrimSpace(m.Body) == "" {
			b.WriteString(`<p class="muted">(无正文)</p>`)
		} else {
			if task != "" && conversationTitle(task) != g.Topic {
				b.WriteString(`<p class="chat-subject">` + esc(conversationTitle(task)) + `</p>`)
			}
			b.WriteString(`<div class="markdown">` + renderMarkdown(m.Body) + `</div>`)
		}

		// Read status indicator
		if !w.DB.IsRead(w.owner(), m.ID) {
			b.WriteString(`<div class="group-msg-unread">未读</div>`)
		}
		b.WriteString(`</div></div></article>`)
	}
	b.WriteString(`</div>`)

	// Footer with mark-read
	b.WriteString(`<footer class="thread-footer">`)
	if unreadCount > 0 {
		fmt.Fprintf(&b, `<span class="muted">%d 条未读消息</span><form method="post" action="/d/groups/%s/ack">`, unreadCount, esc(url.PathEscape(g.ID)))
		for _, m := range messages {
			fmt.Fprintf(&b, `<input type="hidden" name="message" value="%s">`, esc(m.ID))
		}
		fmt.Fprintf(&b, `<button type="submit">全部标为已读（%d）</button></form>`, unreadCount)
	} else {
		b.WriteString(`<span class="muted">本群组全部已读</span><a href="/d/groups">返回群组列表 →</a>`)
	}
	b.WriteString(`</footer></div>`)

	// Member management: only while the group is active, and only for the
	// creator or owner — the same rule the API enforces.
	if g.Status == "active" && w.UpdateGroupMembers != nil {
		b.WriteString(w.groupMembersCard(reg, g))
	}

	b.WriteString(`<style>` + groupCSS + `</style>`)
	page(rw, g.Topic, "/d/groups", b.String())
}

// ackGroup marks all submitted message IDs in a group as read.
func (w *Web) ackGroup(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	g, err := w.DB.GetGroup(id)
	if err != nil || g == nil {
		http.NotFound(rw, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(rw, "无效的表单", http.StatusBadRequest)
		return
	}
	ids := r.PostForm["message"]
	if len(ids) == 0 {
		http.Error(rw, "请刷新后重试", http.StatusBadRequest)
		return
	}
	// Verify all message IDs belong to this group.
	messages, _ := w.DB.ListGroupMessages(g.ID, 0)
	allowed := map[string]bool{}
	for _, m := range messages {
		allowed[m.ID] = true
	}
	for _, mid := range ids {
		if !allowed[mid] {
			http.Error(rw, "消息不属于本群组", http.StatusBadRequest)
			return
		}
	}
	for _, mid := range appendUnique(nil, ids...) {
		if err := w.DB.MarkRead(w.owner(), mid); err != nil {
			http.Error(rw, "标记失败", http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(rw, r, "/d/groups/"+url.PathEscape(g.ID), http.StatusSeeOther)
}

func containsMention(mentions []string, agent string) bool {
	for _, m := range mentions {
		if m == agent {
			return true
		}
	}
	return false
}

// truncateTime returns a short display form of a timestamp string.
func truncateTime(ts string) string {
	if len(ts) >= 16 {
		return ts[5:16] // "MM-DD HH:MM" from "YYYY-MM-DDTHH:MM..."
	}
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}

const groupCSS = `
.group-closed{opacity:.55}.group-closed:hover{opacity:.75}
.group-status-badge{font-size:10px;padding:2px 7px;border-radius:4px;font-weight:500}
.group-status-active{background:var(--soft);color:var(--accent)}
.group-status-closed{background:var(--code);color:var(--muted)}
.group-msg-unread{display:inline-block;font-size:10px;color:var(--accent);font-weight:600;margin-top:8px}
.gf-new{display:inline-flex;align-items:center;min-height:44px;padding:9px 16px;border-radius:7px;background:var(--accent);color:var(--card);font-size:13px;font-weight:550;white-space:nowrap}.gf-new:hover{color:var(--card);filter:brightness(.94)}
.group-form{max-width:640px}.gf-field{display:block;font-size:13px;margin:0 0 18px;border:0;padding:0}.gf-field legend{font-size:13px;padding:0;margin-bottom:6px}
.gf-field select,.gf-field textarea{display:block;width:100%;font:inherit;min-height:44px;padding:10px;border-radius:7px;border:1px solid var(--line);background:var(--bg);color:var(--fg);margin-top:8px}.gf-field textarea{resize:vertical;line-height:1.6}
.gf-choices{display:flex;flex-wrap:wrap;gap:8px}.gf-choice{display:inline-flex;align-items:center;gap:8px;min-height:44px;padding:6px 12px 6px 8px;border:1px solid var(--line);border-radius:22px;cursor:pointer;font-size:12px}.gf-choice:has(input:checked){border-color:var(--accent);background:var(--soft)}.gf-choice input{width:auto;min-height:0;margin:0}.gf-choice .avatar{width:26px;height:26px;font-size:11px}
.group-form button{width:100%;margin-top:6px}
.group-members{margin-top:24px}.group-members>summary{font-size:12px;min-height:44px;padding:12px 0;margin:0}
.gm-list{display:flex;flex-wrap:wrap;gap:10px;margin:4px 0 18px}.gm-member{display:inline-flex;align-items:center;gap:8px;font-size:12px}
.gm-member .avatar{width:26px;height:26px;font-size:11px}
.group-msg-unread:before{content:"";display:inline-block;width:5px;height:5px;border-radius:50%;background:currentColor;margin-right:4px;vertical-align:middle}
`

package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ketchupz1999/agents-home-public/internal/agents"
	"github.com/ketchupz1999/agents-home-public/internal/model"
	"github.com/ketchupz1999/agents-home-public/internal/protocol"
)

// GroupCreateError lets the creator callback tell the form which HTTP status
// and message to show; any other error is treated as an internal failure.
type GroupCreateError interface {
	error
	HTTPStatus() int
}

func (w *Web) groupCSRF() string {
	m := hmac.New(sha256.New, w.SecretKey)
	m.Write([]byte("dashboard|owner|groups"))
	return hex.EncodeToString(m.Sum(nil))
}

// groupForm is what the owner typed, kept so a failed submit re-renders it.
type groupForm struct {
	Item, Topic, Body string
	Members, Mentions []string
	MentionAll        bool
}

// newGroup renders the create-group form.
func (w *Web) newGroup(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Cache-Control", "no-store")
	if w.CreateGroup == nil {
		page(rw, "新建群组", "/d/groups", `<div class="card"><h2>这是只读演示</h2>
<p class="muted">演示实例展示的是合成数据快照，不能新建群组。在自己的实例上，这里可以选择事项、拉成员、写开场消息并 @ 谁来回答。</p>
<p><a href="/d/groups">← 返回群组列表</a></p></div>`)
		return
	}
	f := groupForm{Item: r.URL.Query().Get("item")}
	w.renderGroupForm(rw, http.StatusOK, f, "")
}

// postGroup creates a group as the owner through the same path as
// `POST /v1/groups`, so the opening message and wakeups behave identically.
func (w *Web) postGroup(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(rw, r.Body, 256<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(rw, "表单过大或无法读取", 400)
		return
	}
	if len(r.PostForm["csrf"]) != 1 || !hmac.Equal([]byte(r.PostForm.Get("csrf")), []byte(w.groupCSRF())) {
		http.Error(rw, "页面校验失败，请重新打开建群页", 403)
		return
	}
	f := groupForm{
		Item:     strings.TrimSpace(r.PostForm.Get("item")),
		Topic:    strings.TrimSpace(r.PostForm.Get("topic")),
		Body:     r.PostForm.Get("body"),
		Members:  appendUnique(nil, r.PostForm["member"]...),
		Mentions: appendUnique(nil, r.PostForm["at"]...),
	}
	for _, m := range f.Mentions {
		if m == "all" {
			f.MentionAll = true
		}
	}
	if w.CreateGroup == nil {
		w.renderGroupForm(rw, http.StatusServiceUnavailable, f, "此实例未开放建群。")
		return
	}
	in := protocol.GroupInput{Item: f.Item, Topic: f.Topic, Body: f.Body}
	for _, m := range f.Members {
		if m != w.owner() && m != "" {
			in.Members = append(in.Members, m)
		}
	}
	if f.Topic == "" || f.Item == "" || len(in.Members) == 0 {
		w.renderGroupForm(rw, 400, f, "事项、话题和至少一位成员都要填。")
		return
	}
	if f.MentionAll {
		in.Mentions = []string{"all"}
	} else {
		for _, m := range f.Mentions {
			if m != "" {
				in.Mentions = append(in.Mentions, m)
			}
		}
	}
	g, _, err := w.CreateGroup(w.owner(), in)
	if err != nil {
		var ge GroupCreateError
		if errors.As(err, &ge) {
			w.renderGroupForm(rw, ge.HTTPStatus(), f, groupErrorText(ge.Error()))
			return
		}
		w.renderGroupForm(rw, 500, f, "建群失败，请稍后重试。")
		return
	}
	http.Redirect(rw, r, "/d/groups/"+url.PathEscape(g.ID), http.StatusSeeOther)
}

func groupErrorText(msg string) string {
	switch {
	case msg == "item not found":
		return "事项不存在。"
	case strings.HasPrefix(msg, "invalid member name"):
		return "成员代号不合法：" + strings.TrimPrefix(msg, "invalid member name: ")
	case strings.HasPrefix(msg, "mention not a member"):
		return "@ 的对象必须是群成员：" + strings.TrimPrefix(msg, "mention not a member: ")
	case msg == "item, topic and members required":
		return "事项、话题和至少一位成员都要填。"
	case msg == "group is closed":
		return "群组已关闭，不能再调整成员。"
	case msg == "only creator or owner can change members":
		return "只有创建者或本人可以调整成员。"
	case strings.HasPrefix(msg, "no change"):
		return "成员没有变化。"
	}
	return msg
}

// memberCandidates lists identities that can be put in a group: every
// registered principal plus every key holder, minus the owner itself.
func (w *Web) memberCandidates(reg *agents.Registry) []string {
	var ids []string
	if reg != nil {
		for _, a := range reg.Agents {
			ids = appendUnique(ids, a.ID)
		}
	}
	if w.KeyNames != nil {
		ids = appendUnique(ids, w.KeyNames()...)
	}
	out := ids[:0]
	for _, id := range ids {
		if id != w.owner() && protocol.ValidName(id) {
			out = append(out, id)
		}
	}
	return out
}

func (w *Web) renderGroupForm(rw http.ResponseWriter, status int, f groupForm, problem string) {
	_ = w.Store.Refresh()
	g, gerr := model.Load(w.Store)
	reg, _ := agents.Load(w.Store)
	var b strings.Builder
	b.WriteString(`<div class="thread-page"><a class="back" href="/d/groups">← 返回群组列表</a>`)
	b.WriteString(`<header class="page-heading"><div><p class="eyebrow">把几位 Agent 拉进同一件事</p><h1>新建群组</h1></div></header>`)
	if problem != "" {
		fmt.Fprintf(&b, `<div class="card error" role="alert">%s</div>`, esc(problem))
	}
	fmt.Fprintf(&b, `<form class="card group-form" method="post" action="/d/groups"><input type="hidden" name="csrf" value="%s">`, w.groupCSRF())

	b.WriteString(`<label class="gf-field">事项<select name="item" required><option value="">选择事项</option>`)
	if gerr == nil {
		for _, n := range g.Nodes {
			if n.Type != model.TypeItem {
				continue
			}
			sel := ""
			if n.ID == f.Item {
				sel = " selected"
			}
			fmt.Fprintf(&b, `<option value="%s"%s>%s · %s</option>`, esc(n.ID), sel, esc(n.Title), esc(n.ID))
		}
	}
	b.WriteString(`</select></label>`)

	fmt.Fprintf(&b, `<label class="gf-field">话题<input name="topic" maxlength="120" required value="%s" placeholder="例如：下周要不要换券商"></label>`, esc(f.Topic))

	candidates := w.memberCandidates(reg)
	b.WriteString(`<fieldset class="gf-field"><legend>成员 <span class="muted">（你自动在群里）</span></legend><div class="gf-choices">`)
	for _, id := range candidates {
		v := w.identityAt(reg, id, "")
		fmt.Fprintf(&b, `<label class="gf-choice"><input type="checkbox" name="member" value="%s"%s>%s%s</label>`,
			esc(id), checked(contains(f.Members, id)), avatar(v), identityText(v))
	}
	if len(candidates) == 0 {
		b.WriteString(`<p class="muted">还没有可加入的 Agent。</p>`)
	}
	b.WriteString(`</div></fieldset>`)

	fmt.Fprintf(&b, `<label class="gf-field">开场消息<textarea name="body" rows="7" placeholder="背景、问题、希望谁给出什么（支持 Markdown）">%s</textarea></label>`, esc(f.Body))

	b.WriteString(`<fieldset class="gf-field"><legend>@ 谁来回答 <span class="muted">（被 @ 的成员会收到唤醒任务；不 @ 就只是一条通知）</span></legend><div class="gf-choices">`)
	fmt.Fprintf(&b, `<label class="gf-choice"><input type="checkbox" name="at" value="all"%s><b>全部成员</b></label>`, checked(f.MentionAll))
	for _, id := range candidates {
		v := w.identityAt(reg, id, "")
		fmt.Fprintf(&b, `<label class="gf-choice"><input type="checkbox" name="at" value="%s"%s>@%s</label>`,
			esc(id), checked(!f.MentionAll && contains(f.Mentions, id)), esc(v.Name))
	}
	b.WriteString(`</div></fieldset>`)

	b.WriteString(`<p class="muted">建好后还能在群组页面增删成员（只影响之后的消息）。</p>`)
	b.WriteString(`<button type="submit">创建并发送开场消息</button></form></div>`)
	b.WriteString(`<style>` + groupCSS + `</style>`)
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.WriteHeader(status)
	page(rw, "新建群组", "/d/groups", b.String())
}

func checked(v bool) string {
	if v {
		return " checked"
	}
	return ""
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

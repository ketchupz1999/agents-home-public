package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ketchupz1999/agents-home-public/internal/agents"
	"github.com/ketchupz1999/agents-home-public/internal/protocol"
)

func (w *Web) groupMembersCSRF(groupID string) string {
	m := hmac.New(sha256.New, w.SecretKey)
	m.Write([]byte("dashboard|owner|group-members|" + groupID))
	return hex.EncodeToString(m.Sum(nil))
}

// groupMembersCard renders who is in the room plus the add/remove controls.
func (w *Web) groupMembersCard(reg *agents.Registry, g *protocol.Group) string {
	var b strings.Builder
	b.WriteString(`<details class="card group-members"><summary>成员管理（` + fmt.Sprint(len(g.Members)) + ` 位）</summary>`)
	b.WriteString(`<div class="gm-list">`)
	for _, id := range g.Members {
		v := w.identityAt(reg, id, "")
		b.WriteString(`<span class="gm-member">` + avatar(v) + identityText(v) + `</span>`)
	}
	b.WriteString(`</div>`)
	if w.UpdateGroupMembers == nil {
		b.WriteString(`<p class="muted">此实例未开放成员调整。</p></details>`)
		return b.String()
	}
	// The owner can leave the room; the creator cannot remove themselves by
	// accident, so owner is offered but creator is not marked required.
	inGroup := map[string]bool{}
	for _, id := range g.Members {
		inGroup[id] = true
	}
	candidates := w.memberCandidates(reg)
	b.WriteString(`<form method="post" action="/d/groups/` + esc(g.ID) + `/members"><input type="hidden" name="csrf" value="` + w.groupMembersCSRF(g.ID) + `">`)
	b.WriteString(`<fieldset class="gf-field"><legend>移出</legend><div class="gf-choices">`)
	for _, id := range g.Members {
		if id == w.owner() {
			continue // you can always read your own room
		}
		v := w.identityAt(reg, id, "")
		fmt.Fprintf(&b, `<label class="gf-choice"><input type="checkbox" name="remove" value="%s">%s%s</label>`, esc(id), avatar(v), identityText(v))
	}
	b.WriteString(`</div></fieldset><fieldset class="gf-field"><legend>加入</legend><div class="gf-choices">`)
	any := false
	for _, id := range candidates {
		if inGroup[id] {
			continue
		}
		any = true
		v := w.identityAt(reg, id, "")
		fmt.Fprintf(&b, `<label class="gf-choice"><input type="checkbox" name="add" value="%s">%s%s</label>`, esc(id), avatar(v), identityText(v))
	}
	if !any {
		b.WriteString(`<p class="muted">没有可加入的 Agent。</p>`)
	}
	b.WriteString(`</div></fieldset><p class="muted">被移出的人立刻读不到本群，手上未处理的任务会被取消；新加入的人能读到本群之前的全部消息。</p>`)
	b.WriteString(`<button type="submit">保存成员</button></form></details>`)
	return b.String()
}

// postGroupMembers applies the form through the server method, so the web and
// the API cannot drift apart on who may change membership.
func (w *Web) postGroupMembers(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("id")
	r.Body = http.MaxBytesReader(rw, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(rw, "表单过大或无法读取", 400)
		return
	}
	if len(r.PostForm["csrf"]) != 1 || !hmac.Equal([]byte(r.PostForm.Get("csrf")), []byte(w.groupMembersCSRF(id))) {
		http.Error(rw, "页面校验失败，请重新打开群组", 403)
		return
	}
	g, err := w.DB.GetGroup(id)
	if err != nil || g == nil {
		http.NotFound(rw, r)
		return
	}
	in := protocol.GroupMemberInput{
		Add:    appendUnique(nil, r.PostForm["add"]...),
		Remove: appendUnique(nil, r.PostForm["remove"]...),
	}
	if len(in.Add) == 0 && len(in.Remove) == 0 {
		http.Redirect(rw, r, "/d/groups/"+id, http.StatusSeeOther)
		return
	}
	if w.UpdateGroupMembers == nil {
		http.Error(rw, "此实例未开放成员调整", http.StatusServiceUnavailable)
		return
	}
	if _, _, _, _, err = w.UpdateGroupMembers(w.owner(), id, in); err != nil {
		var ge GroupCreateError
		status, msg := 500, "调整失败，请稍后重试。"
		if errors.As(err, &ge) {
			status, msg = ge.HTTPStatus(), groupErrorText(err.Error())
		}
		page(rw, "群组", "/d/groups", `<div class="card error" role="alert">`+esc(msg)+`</div><p><a href="/d/groups/`+esc(id)+`">← 返回群组</a></p>`)
		rw.WriteHeader(status)
		return
	}
	http.Redirect(rw, r, "/d/groups/"+id, http.StatusSeeOther)
}

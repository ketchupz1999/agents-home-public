package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/ketchupz1999/agents-home-public/internal/db"
	"github.com/ketchupz1999/agents-home-public/internal/model"
)

func (w *Web) focusCSRF() string {
	m := hmac.New(sha256.New, w.SecretKey)
	m.Write([]byte("dashboard|owner|focus"))
	return hex.EncodeToString(m.Sum(nil))
}

func (w *Web) saveFocus(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(rw, r.Body, 8192)
	if err := r.ParseForm(); err != nil {
		http.Error(rw, "表单过大或无法读取", 400)
		return
	}
	if len(r.PostForm["csrf"]) != 1 || !hmac.Equal([]byte(r.PostForm.Get("csrf")), []byte(w.focusCSRF())) {
		http.Error(rw, "页面校验失败，请重新打开总览", 403)
		return
	}
	if len(r.PostForm["item"]) > 2 {
		http.Error(rw, "最多选择两个事项", 400)
		return
	}
	items := []string{}
	for _, id := range r.PostForm["item"] {
		if id != "" {
			items = append(items, id)
		}
	}
	base, err := strconv.ParseInt(r.PostForm.Get("base_revision"), 10, 64)
	if len(r.PostForm["base_revision"]) != 1 || err != nil || base < 0 {
		http.Error(rw, "页面版本无效，请重新打开总览", 400)
		return
	}
	if err = w.Store.Refresh(); err != nil {
		http.Error(rw, "事项读取失败，请稍后重试", 503)
		return
	}
	g, err := model.Load(w.Store)
	if err != nil {
		http.Error(rw, "事项读取失败，请稍后重试", 503)
		return
	}
	validation := db.ValidateFocus(items)
	if validation == nil {
		for _, id := range items {
			if n := g.Get("item:" + id); n == nil {
				validation = fmt.Errorf("事项 %s 已不存在，请重新选择", id)
				break
			}
		}
	}
	if validation != nil {
		w.focusFailure(rw, g, items, 400, validation.Error())
		return
	}
	_, err = w.DB.SaveFocus(items, base)
	if errors.Is(err, db.ErrFocusConflict) {
		w.focusFailure(rw, g, items, 409, "其他页面已调整关注。请核对当前值，再保存你的选择。")
		return
	}
	if err != nil {
		http.Error(rw, "保存结果未确认，请刷新总览核对后重试", 503)
		return
	}
	http.Redirect(rw, r, "/d/", http.StatusSeeOther)
}

func (w *Web) focusFailure(rw http.ResponseWriter, g *model.Graph, items []string, status int, message string) {
	current, err := w.DB.GetFocus()
	if err != nil {
		http.Error(rw, "关注读取失败，请稍后重试", 503)
		return
	}
	var b strings.Builder
	b.WriteString(`<style>` + overviewCSS + `</style><section class="overview focus-recovery"><a class="back" href="/d/">← 返回总览</a><h1>调整关注</h1>`)
	fmt.Fprintf(&b, `<p role="alert">%s</p><p class="muted">当前关注：%s</p>`, esc(message), esc(focusNames(g, current.Items)))
	b.WriteString(w.focusForm(g, items, current.Revision))
	b.WriteString(`</section><script>` + overviewJS + `</script>`)
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.WriteHeader(status)
	page(rw, "调整关注", "/d/", b.String())
}

func focusNames(g *model.Graph, items []string) string {
	names := []string{}
	for _, id := range items {
		if n := g.Get("item:" + id); n != nil {
			names = append(names, n.Title)
		} else {
			names = append(names, id+"（已不存在）")
		}
	}
	if len(names) == 0 {
		return "无"
	}
	return strings.Join(names, " → ")
}

// Native selects also preserve explicit primary/secondary order without JS.
func (w *Web) focusForm(g *model.Graph, selected []string, revision int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<form class="focus-form" method="post" action="/d/focus"><input type="hidden" name="csrf" value="%s"><input type="hidden" name="base_revision" value="%d">`, w.focusCSRF(), revision)
	for i, label := range []string{"首要关注", "其次关注"} {
		value := ""
		if i < len(selected) {
			value = selected[i]
		}
		fmt.Fprintf(&b, `<label class="focus-field">%s<select name="item" data-focus-slot><option value="">不置顶</option>`, label)
		if value != "" && g.Get("item:"+value) == nil {
			fmt.Fprintf(&b, `<option value="%s" selected>%s（已不存在）</option>`, esc(value), esc(value))
		}
		for _, n := range g.Nodes {
			if n.Type != model.TypeItem {
				continue
			}
			sel := ""
			if n.ID == value {
				sel = " selected"
			}
			fmt.Fprintf(&b, `<option value="%s"%s>%s</option>`, esc(n.ID), sel, esc(n.Title))
		}
		b.WriteString(`</select></label>`)
	}
	b.WriteString(`<p class="focus-form-error" role="alert" hidden></p><button class="focus-save" type="submit">保存关注</button></form>`)
	return b.String()
}

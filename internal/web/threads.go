package web

import (
	"regexp"
	"sort"
	"strings"

	"github.com/ketchupz1999/agents-home-public/internal/protocol"
)

type conversation struct {
	RootID, Title       string
	Messages            []protocol.Message
	Participants, Items []string
	Unread              int
}

var replyPrefix = regexp.MustCompile(`(?i)^\s*re\s*:\s*`)

func conversationTitle(task string) string {
	for replyPrefix.MatchString(task) {
		task = replyPrefix.ReplaceAllString(task, "")
	}
	if task = strings.TrimSpace(task); task == "" {
		return "未命名会话"
	}
	return task
}

// Group before filtering: a read parent or a reply under another item is still
// part of the same conversation. Missing parents use a stable virtual root;
// malformed cycles share their lexicographically first ID, without hanging.
func groupConversations(messages []protocol.Message, isRead func(string) bool) []conversation {
	messages = append([]protocol.Message(nil), messages...)
	sort.Slice(messages, func(i, j int) bool {
		if messages[i].CreatedAt.Equal(messages[j].CreatedAt) {
			return messages[i].ID < messages[j].ID
		}
		return messages[i].CreatedAt.Before(messages[j].CreatedAt)
	})
	byID := map[string]protocol.Message{}
	for _, m := range messages {
		byID[m.ID] = m
	}
	roots := map[string]string{}
	rootOf := func(id string) string {
		path, seen := []string{}, map[string]int{}
		root := id
		for {
			if cached, ok := roots[root]; ok {
				root = cached
				break
			}
			if start, ok := seen[root]; ok {
				cycle := append([]string(nil), path[start:]...)
				sort.Strings(cycle)
				root = cycle[0]
				break
			}
			seen[root] = len(path)
			path = append(path, root)
			m, ok := byID[root]
			if !ok || m.ReplyTo == "" {
				break
			}
			root = m.ReplyTo
		}
		for _, member := range path {
			roots[member] = root
		}
		return root
	}
	grouped := map[string]*conversation{}
	var order []string
	for _, m := range messages {
		root := rootOf(m.ID)
		c := grouped[root]
		if c == nil {
			title := m.Task
			if parent, ok := byID[root]; ok {
				title = parent.Task
			}
			c = &conversation{RootID: root, Title: conversationTitle(title)}
			grouped[root] = c
			order = append(order, root)
		}
		c.Messages = append(c.Messages, m)
		c.Participants = appendUnique(c.Participants, m.From, m.To)
		c.Items = appendUnique(c.Items, m.Item)
		if !isRead(m.ID) {
			c.Unread++
		}
	}
	result := make([]conversation, 0, len(order))
	for _, root := range order {
		result = append(result, *grouped[root])
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i].Messages[len(result[i].Messages)-1], result[j].Messages[len(result[j].Messages)-1]
		if a.CreatedAt.Equal(b.CreatedAt) {
			return a.ID > b.ID
		}
		return a.CreatedAt.After(b.CreatedAt)
	})
	return result
}

func appendUnique(values []string, more ...string) []string {
	for _, candidate := range more {
		if candidate == "" {
			continue
		}
		found := false
		for _, v := range values {
			if v == candidate {
				found = true
				break
			}
		}
		if !found {
			values = append(values, candidate)
		}
	}
	return values
}

const threadCSS = `
.avatar{position:relative;overflow:hidden}.avatar img,.avatar svg{position:absolute;inset:0;width:100%;height:100%;background:inherit}.identity-label{overflow-wrap:anywhere;min-width:0}.agent-directory{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px}.agent-heading{display:flex;align-items:center;gap:12px}.agent-card{min-width:0;overflow-wrap:anywhere;margin:0;padding:24px}.agent-card .avatar{width:56px;height:56px;font-size:22px}.identity-badge{display:inline-flex;align-items:center;gap:10px;min-width:0}.identity-text{display:inline-flex;flex-direction:column;gap:3px;min-width:0;overflow-wrap:anywhere}.identity-name{font-weight:600;font-size:13px;color:var(--fg)}.identity-id{font:10px/1.5 ui-monospace,SFMono-Regular,Menlo,monospace;color:var(--muted);font-weight:400}.agent-card .identity-name{font-size:17px}.agent-card .identity-id{font-size:11px}.agent-context{display:flex;gap:8px;margin:20px 0 12px}.agent-context>span{color:var(--muted);background:var(--soft);border-radius:4px;padding:2px 7px;font-size:11px}.agent-card>p:last-child{font-size:13px;color:var(--muted);line-height:1.8;margin-bottom:0}.chat-byline>.identity-text{margin-right:auto}.chat-recipient{font-size:10px}.chat-byline .identity-name{font-size:12px}.thread-participants>span{min-width:0;max-width:100%;overflow-wrap:anywhere}

.avatar{display:inline-flex;align-items:center;justify-content:center;flex-shrink:0;width:36px;height:36px;border-radius:50%;font-size:15px;font-weight:600;line-height:1;letter-spacing:0}.avatar-stack{display:flex;padding-left:0;min-width:76px}.avatar-stack .avatar{border:3px solid var(--card);width:36px;height:36px;margin-left:-10px}.avatar-stack .avatar:first-child{margin-left:0}.avatar-more{background:var(--soft);color:var(--muted);font-size:10px}
.conversation-list{border:1px solid var(--line);border-radius:12px;background:var(--card);overflow:hidden}.conversation-list:empty{display:none}.conversation-row{display:flex;align-items:flex-start;gap:16px;padding:23px 24px}.conversation-row+.conversation-row{border-top:1px solid var(--line)}.conversation-row:hover{background:var(--soft);color:var(--fg)}.conversation-main{flex:1;min-width:0}.conversation-top{display:flex;align-items:baseline;justify-content:space-between;gap:20px}.conversation-top h2{font-size:15px;font-weight:600;line-height:1.6;margin:0;overflow-wrap:anywhere}.conversation-top time{white-space:nowrap;font-size:11px;color:var(--muted);flex-shrink:0}.conversation-people{margin:5px 0 10px;font-size:11px;color:var(--muted);overflow-wrap:anywhere}.conversation-preview{margin:0 0 13px;color:var(--muted);font-size:13px;line-height:1.7;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;overflow-wrap:anywhere}.conversation-preview b{font-weight:500;color:var(--fg)}.conversation-bottom{display:flex;align-items:center;gap:10px;font-size:10px;color:var(--muted);flex-wrap:wrap}.conversation-item{background:var(--soft);padding:2px 7px;border-radius:4px;overflow-wrap:anywhere}.unread-count{color:var(--accent);font-weight:600;margin-left:auto}.unread-count:before{content:"";display:inline-block;width:5px;height:5px;border-radius:50%;background:currentColor;margin-right:5px;vertical-align:middle}.conversation-arrow{align-self:center;color:var(--muted);font-size:20px}
.thread-page{max-width:850px;margin:auto}.thread-heading{padding:5px 0 25px;border-bottom:1px solid var(--line)}.thread-heading h1{font-size:24px;line-height:1.5;letter-spacing:0}.thread-context{display:flex;gap:12px;flex-wrap:wrap;font-size:11px;color:var(--muted);margin:12px 0 20px}.thread-context a{color:var(--accent)}.thread-participants{display:flex;flex-wrap:wrap;gap:18px}.thread-participants>span{display:flex;align-items:center;gap:7px;font-size:12px}.thread-participants b{font-weight:500}.thread-participants .avatar{width:26px;height:26px;font-size:12px}.chat-day{display:flex;align-items:center;justify-content:center;gap:16px;margin:26px 0;font-size:10px;color:var(--muted)}.chat-day:before,.chat-day:after{content:"";height:1px;width:36px;background:var(--line)}.chat-message{display:flex;gap:12px;margin:0 0 26px;scroll-margin-top:20px}.chat-message>.avatar{margin-top:3px}.chat-content{min-width:0;flex:1;max-width:calc(100% - 60px)}.chat-byline{display:flex;align-items:baseline;gap:8px;flex-wrap:wrap;margin:0 0 8px;font-size:11px;color:var(--muted);overflow-wrap:anywhere}.chat-byline strong{font-size:12px;font-weight:600;color:var(--fg)}.chat-byline time{font-size:10px;margin-left:auto;white-space:nowrap}.chat-bubble{background:var(--card);border:1px solid var(--line);border-radius:3px 12px 12px 12px;padding:18px 20px;overflow-wrap:anywhere}.chat-subject{font-weight:600;font-size:14px;margin:0 0 12px}.chat-kind{font-size:10px;color:var(--muted);margin-bottom:12px}.outgoing{flex-direction:row-reverse}.outgoing .chat-bubble{background:var(--soft);border-radius:12px 3px 12px 12px}.relay .chat-bubble{border-left:2px solid var(--map-wire,var(--line))}.reply-context{display:block;border-left:2px solid var(--line);background:var(--code);padding:8px 12px;margin:0 0 16px;border-radius:0 5px 5px 0;color:var(--muted);font-size:11px}.reply-context span{display:block;margin-top:3px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--muted)}.thread-footer{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap;border-top:1px solid var(--line);padding:22px 0 0;font-size:12px}
.markdown{font-size:14px;line-height:1.85;overflow-wrap:anywhere}.markdown>:first-child{margin-top:0}.markdown>:last-child{margin-bottom:0}.markdown p{margin:12px 0}.markdown h2,.markdown h3,.markdown h4{font-size:16px;line-height:1.65;margin:23px 0 10px;color:var(--fg);letter-spacing:0}.markdown h3{font-size:15px}.markdown h4{font-size:14px}.markdown ul,.markdown ol{padding-left:22px;margin:12px 0}.markdown li{margin:6px 0}.markdown strong{font-weight:600}.markdown code{font:12px/1.6 ui-monospace,SFMono-Regular,Menlo,monospace;background:var(--code);border-radius:4px;padding:2px 4px;overflow-wrap:anywhere}.markdown pre{white-space:pre;overflow:auto;max-width:100%;padding:14px;margin:14px 0}.markdown pre code{padding:0;background:transparent;white-space:pre;overflow-wrap:normal}.markdown blockquote{margin:14px 0;padding-left:14px;border-left:2px solid var(--line);color:var(--muted)}.markdown-table{overflow:auto;max-width:100%;margin:16px 0;border:1px solid var(--line);border-radius:7px}.markdown table{border-collapse:collapse;width:100%;min-width:440px;font-size:12px;line-height:1.7}.markdown th,.markdown td{padding:9px 11px;text-align:left;border-bottom:1px solid var(--line);vertical-align:top;min-width:90px}.markdown th{font-weight:600;background:var(--soft)}.markdown tr:last-child td{border-bottom:0}.markdown hr{border:0;border-top:1px solid var(--line);margin:20px 0}
@media(max-width:640px){.agent-directory{grid-template-columns:1fr;gap:10px}.agent-card{padding:20px}.agent-card .avatar{width:48px;height:48px}.chat-byline>.identity-text{flex-basis:100%}.conversation-row{padding:18px 14px;gap:10px;flex-wrap:wrap}.conversation-row>.avatar-stack{min-width:0;width:100%;height:28px}.avatar-stack .avatar{height:29px;width:29px;font-size:12px}.conversation-main{flex-basis:calc(100% - 24px)}.conversation-top{display:block}.conversation-top h2{font-size:14px;line-height:1.7}.conversation-top time{display:block;margin:5px 0}.conversation-people{font-size:11px;margin:5px 0 10px}.conversation-preview{font-size:12px}.conversation-arrow{align-self:flex-end}.thread-heading h1{font-size:20px}.thread-heading{padding-bottom:20px}.thread-participants{gap:12px}.chat-message{gap:8px;margin-bottom:24px}.chat-message>.avatar{width:28px;height:28px;font-size:12px}.chat-content{max-width:calc(100% - 36px)}.chat-byline{gap:5px;font-size:10px}.chat-byline strong{font-size:11px}.chat-byline time{margin-left:0}.chat-bubble{padding:14px 12px}.markdown{font-size:13px;line-height:1.85}.markdown h2,.markdown h3{font-size:14px}.markdown li{margin:7px 0}.reply-context{padding:7px 9px}.thread-context{gap:8px}.thread-footer{align-items:stretch;flex-direction:column}.thread-footer button{width:100%}}
`

package web

import (
	"github.com/ketchupz1999/agents-home-public/internal/protocol"
	"net/url"
)

func messageURL(m protocol.Message) string {
	if m.GroupID != "" {
		return "/d/groups/" + url.PathEscape(m.GroupID)
	}
	// Legacy mailbox records remain in activity history, but the retired
	// thread route must not be presented as a working detail page.
	return "/d/groups"
}

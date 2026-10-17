package web

import (
	"strings"
	"testing"
	"time"

	"github.com/ketchupz1999/agents-home-public/internal/protocol"
)

func TestDashboardLinksUseCurrentGroupRoutes(t *testing.T) {
	w := testThreadWeb(t)
	m := protocol.Message{ID: "test-message", GroupID: "grp_test", From: "helper", To: "group:grp_test", Item: "english", Task: "test group", Body: "body", Kind: "note", CreatedAt: time.Now().Add(-time.Minute)}
	if err := w.DB.PutGroupMessage(m); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/d/", "/d/agents?view=activity&scope=all"} {
		rec := requestThread(t, w, "GET", path, nil, true)
		if rec.Code != 200 {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), `href="/d/inbox`) || strings.Contains(rec.Body.String(), `href="/d/thread/`) {
			t.Errorf("%s links to retired mailbox", path)
		}
	}
}

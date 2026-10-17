package server

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRemovedCreatorCannotCloseGroup(t *testing.T) {
	e := setup(t)
	_, res, _ := e.do(t, "claude-code", "POST", "/v1/groups", map[string]any{"item": "english", "topic": "close authorization", "members": []string{"owner"}, "body": "seed"})
	gid := res["group"].(map[string]any)["id"].(string)
	code, _, _ := e.do(t, "owner", "POST", "/v1/groups/"+gid+"/members", map[string]any{"remove": []string{"claude-code"}})
	if code != 200 {
		t.Fatalf("remove: %d", code)
	}
	code, _, _ = e.do(t, "claude-code", "POST", "/v1/groups/"+gid+"/close", nil)
	if code < 400 {
		t.Errorf("removed creator closed group: %d", code)
	}
	group, err := e.db.GetGroup(gid)
	if err != nil {
		t.Fatal(err)
	}
	if group.Status != "active" {
		t.Error("removed creator changed group status")
	}
}

func TestActionsRespectSourceGroupMembership(t *testing.T) {
	e := setup(t)
	_, g, _ := e.do(t, "claude-code", "POST", "/v1/groups", map[string]any{"item": "english", "topic": "private", "members": []string{"assistant-cloud"}, "body": "secret"})
	gid := g["group"].(map[string]any)["id"].(string)
	p := map[string]any{"client_id": "private-action", "item": "english", "title": "PRIVATE ACTION", "plan_kind": "later", "source_message_id": g["root_message_id"]}
	code, a, _ := e.do(t, "muse", "POST", "/v1/actions", p)
	if code != 201 {
		t.Fatalf("seed %d %v", code, a)
	}
	id := a["id"].(string)
	for _, caller := range []string{"limited", "muse"} {
		if caller == "muse" {
			if _, err := e.db.RemoveGroupMember(gid, "assistant-cloud"); err != nil {
				t.Fatal(err)
			}
		}
		code, _, rows := e.do(t, caller, "GET", "/v1/actions?item=english", nil)
		if code != 200 || len(rows) != 0 {
			t.Errorf("%s private actions visible", caller)
		}
		code, _, _ = e.do(t, caller, "GET", "/v1/actions/"+id, nil)
		if code != 404 {
			t.Errorf("%s private action detail status %d", caller, code)
		}
		code, _, _ = e.do(t, caller, "POST", "/v1/actions", p)
		if code != 404 {
			t.Errorf("%s creation/retry after denied membership returned %d", caller, code)
		}
	}
	code, _, _ = e.do(t, "owner", "GET", "/v1/actions/"+id, nil)
	if code != 200 {
		t.Fatalf("owner denied: %d", code)
	}
}

func TestBranchReadScopesCoverDiffAndMetadata(t *testing.T) {
	e := setup(t)
	e.pushBranch(t, "agent/claude-code/secret", map[string]string{"items/secret/CONTEXT.md": "# PRIVATEPATCH\n"})
	code, _, rows := e.do(t, "limited", "GET", "/v1/branches", nil)
	if code != 200 || len(rows) != 0 {
		t.Errorf("restricted key can enumerate private branch")
	}
	req, _ := http.NewRequest("GET", e.ts.URL+"/v1/branches/agent/claude-code/secret/diff", nil)
	req.Header.Set("Authorization", "Bearer "+e.keys["limited"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("restricted diff returned %d", resp.StatusCode)
	}
	req.Header.Set("Authorization", "Bearer "+e.keys["owner"])
	allowed, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer allowed.Body.Close()
	if allowed.StatusCode != 200 {
		t.Errorf("owner diff denied %d", allowed.StatusCode)
	}
}

func TestMembershipChangeRollsBackAndHonorsClosedGroup(t *testing.T) {
	e := setup(t)
	_, res, _ := e.do(t, "owner", "POST", "/v1/groups", map[string]any{"item": "english", "topic": "membership transaction", "members": []string{"assistant-cloud"}, "body": "seed", "mentions": []string{"assistant-cloud"}})
	gid := res["group"].(map[string]any)["id"].(string)
	if _, err := e.db.Exec(`CREATE TRIGGER deny_member_note BEFORE INSERT ON messages WHEN NEW.kind='note' BEGIN SELECT RAISE(ABORT,'note failed'); END`); err != nil {
		t.Fatal(err)
	}
	code, _, _ := e.do(t, "owner", "POST", "/v1/groups/"+gid+"/members", map[string]any{"remove": []string{"assistant-cloud"}})
	if code < 400 {
		t.Fatalf("forced failure returned %d", code)
	}
	if !e.db.IsGroupMember(gid, "assistant-cloud") {
		t.Error("failed member change removed membership")
	}
	pending, err := e.db.PendingWakeups("assistant-cloud", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Error("failed member change cancelled task")
	}
}

func TestDelayedGroupSendCannotOutliveRevocation(t *testing.T) {
	for _, scenario := range []string{"remove-sender", "close", "remove-mentioned"} {
		t.Run(scenario, func(t *testing.T) {
			e := setup(t)
			_, res, _ := e.do(t, "owner", "POST", "/v1/groups", map[string]any{"item": "english", "topic": "race", "members": []string{"assistant-cloud", "claude-code"}, "body": "seed"})
			gid := res["group"].(map[string]any)["id"].(string)
			body := `{"body":"must not survive revocation","mentions":["claude-code"]}`
			conn, err := net.Dial("tcp", strings.TrimPrefix(e.ts.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(10 * time.Second))
			fmt.Fprintf(conn, "POST /v1/groups/%s/messages HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nExpect: 100-continue\r\n\r\n", gid, e.keys["muse"], len(body))
			reader := bufio.NewReader(conn)
			first, err := http.ReadResponse(reader, nil)
			if err != nil {
				t.Fatal(err)
			}
			if first.StatusCode != 100 {
				t.Fatalf("expected body-read boundary, got %d", first.StatusCode)
			}
			if scenario == "close" {
				code, _, _ := e.do(t, "owner", "POST", "/v1/groups/"+gid+"/close", nil)
				if code != 200 {
					t.Fatal(code)
				}
			} else {
				remove := "assistant-cloud"
				if scenario == "remove-mentioned" {
					remove = "claude-code"
				}
				code, _, _ := e.do(t, "owner", "POST", "/v1/groups/"+gid+"/members", map[string]any{"remove": []string{remove}})
				if code != 200 {
					t.Fatal(code)
				}
			}
			before := e.db.CountGroupMessages(gid)
			io.WriteString(conn, body)
			response, err := http.ReadResponse(reader, nil)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode < 400 {
				t.Errorf("delayed send accepted: %d", response.StatusCode)
			}
			if e.db.CountGroupMessages(gid) != before {
				t.Error("delayed send inserted message after revocation")
			}
			pending, err := e.db.PendingWakeups("claude-code", 50)
			if err != nil {
				t.Fatal(err)
			}
			if len(pending) != 0 {
				t.Error("delayed send created wakeup after revocation")
			}
		})
	}
}

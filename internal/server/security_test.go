package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFinishRejectsInvalidClaimsWithoutSideEffects(t *testing.T) {
	for _, scenario := range []string{"bad-token", "wrong-recipient", "removed", "closed", "duplicate", "insert-fails", "failed-wrong-recipient"} {
		t.Run(scenario, func(t *testing.T) {
			e := setup(t)
			code, res, _ := e.do(t, "claude-code", "POST", "/v1/groups", map[string]any{"item": "english", "topic": "private", "members": []string{"assistant-cloud"}, "body": "question", "mentions": []string{"assistant-cloud"}})
			if code != 201 {
				t.Fatalf("seed group %d", code)
			}
			gid := res["group"].(map[string]any)["id"].(string)
			_, _, tasks := e.do(t, "muse", "GET", "/v1/wakeups", nil)
			wid := tasks[0].(map[string]any)["id"].(string)
			code, claimed, _ := e.do(t, "muse", "POST", "/v1/wakeups/"+wid+"/claim", nil)
			if code != 200 {
				t.Fatalf("claim %d", code)
			}
			token := claimed["claim_token"].(string)
			caller, result := "muse", "replied"
			switch scenario {
			case "bad-token":
				token = "invalid"
			case "wrong-recipient":
				caller = "limited"
			case "failed-wrong-recipient":
				caller = "limited"
				result = "failed"
			case "removed":
				if _, err := e.db.RemoveGroupMember(gid, "assistant-cloud"); err != nil {
					t.Fatal(err)
				}
			case "closed":
				if err := e.db.CloseGroup(gid, "owner", true); err != nil {
					t.Fatal(err)
				}
			case "duplicate":
				code, _, _ = e.do(t, "muse", "POST", "/v1/wakeups/"+wid+"/finish", map[string]any{"claim_token": token, "result": "replied", "body": "first"})
				if code != 200 {
					t.Fatalf("first finish %d", code)
				}
			case "insert-fails":
				if _, err := e.db.Exec(`CREATE TRIGGER reject_reply BEFORE INSERT ON messages WHEN NEW.kind='reply' BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			before := e.db.CountGroupMessages(gid)
			wkBefore, _ := e.db.GetWakeup(wid)
			code, _, _ = e.do(t, caller, "POST", "/v1/wakeups/"+wid+"/finish", map[string]any{"claim_token": token, "result": result, "body": "must not persist"})
			if code < 400 {
				t.Errorf("rejected finish returned %d", code)
			}
			if after := e.db.CountGroupMessages(gid); after != before {
				t.Errorf("rejected finish wrote message: before=%d after=%d", before, after)
			}
			wkAfter, _ := e.db.GetWakeup(wid)
			if wkBefore.Status != wkAfter.Status {
				t.Errorf("rejected finish changed status %s -> %s", wkBefore.Status, wkAfter.Status)
			}
		})
	}
}

func TestGroupReadBoundariesInSearchBriefAndUnread(t *testing.T) {
	e := setup(t)
	code, res, _ := e.do(t, "claude-code", "POST", "/v1/groups", map[string]any{"item": "english", "topic": "RESTRICTEDAUDITTOPIC", "members": []string{"assistant-cloud"}, "body": "RESTRICTEDAUDITBODY"})
	if code != 201 {
		t.Fatalf("seed %d", code)
	}
	gid := res["group"].(map[string]any)["id"].(string)
	for _, caller := range []string{"limited", "muse", "owner"} {
		if caller == "muse" {
			if _, err := e.db.RemoveGroupMember(gid, "assistant-cloud"); err != nil {
				t.Fatal(err)
			}
		}
		for _, endpoint := range []string{"/v1/search?q=RESTRICTEDAUDIT", "/v1/brief/english"} {
			code, obj, _ := e.do(t, caller, "GET", endpoint, nil)
			var payload any = obj
			if strings.HasPrefix(endpoint, "/v1/search") {
				payload = obj["hits"]
			}
			data, _ := json.Marshal(payload)
			if code != 200 {
				t.Fatalf("%s %s status %d", caller, endpoint, code)
			}
			visible := strings.Contains(string(data), "RESTRICTEDAUDIT")
			if visible != (caller == "owner") {
				t.Errorf("%s %s private content visible=%v", caller, endpoint, visible)
			}
		}
		_, _, items := e.do(t, caller, "GET", "/v1/items", nil)
		for _, row := range items {
			item := row.(map[string]any)
			if item["id"] == "english" && caller != "owner" && item["unread"].(float64) != 0 {
				t.Errorf("%s private unread count leaked", caller)
			}
		}
	}
}

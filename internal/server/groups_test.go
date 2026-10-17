package server

import (
	"testing"
)

func TestGroupDiscussion(t *testing.T) {
	e := setup(t)

	// 1. Create a group with 3 members.
	code, res, _ := e.do(t, "claude-code", "POST", "/v1/groups", map[string]any{
		"item":     "english",
		"topic":    "户外市场讨论",
		"members":  []string{"assistant-cloud", "owner"},
		"body":     "请各自给出一个建议。",
		"mentions": []string{"all"},
	})
	if code != 201 {
		t.Fatalf("create group: %d %v", code, res)
	}
	group := res["group"].(map[string]any)
	groupID := group["id"].(string)
	rootMsgID := res["root_message_id"].(string)
	if groupID == "" || rootMsgID == "" {
		t.Fatalf("missing group_id or root_message_id")
	}
	members := group["members"].([]any)
	if len(members) != 3 { // claude-code (creator) + assistant-cloud + owner
		t.Fatalf("expected 3 members, got %d: %v", len(members), members)
	}

	// 2. All members can see the group.
	code, _, arr := e.do(t, "muse", "GET", "/v1/groups", nil)
	if code != 200 || len(arr) != 1 {
		t.Fatalf("muse list groups: %d, count=%d", code, len(arr))
	}

	// 3. Non-member cannot see the group.
	code, _, arr = e.do(t, "limited", "GET", "/v1/groups", nil)
	if len(arr) != 0 {
		t.Fatalf("limited should see 0 groups, got %d", len(arr))
	}
	code, _, _ = e.do(t, "limited", "GET", "/v1/groups/"+groupID, nil)
	if code != 404 {
		t.Fatalf("limited get group: %d (expected 404)", code)
	}

	// 4. Members can read group messages.
	code, _, arr = e.do(t, "muse", "GET", "/v1/groups/"+groupID+"/messages", nil)
	if code != 200 || len(arr) != 1 {
		t.Fatalf("muse read messages: %d, count=%d", code, len(arr))
	}

	// 5. Send a message into the group (muse replies).
	code, res, _ = e.do(t, "muse", "POST", "/v1/groups/"+groupID+"/messages", map[string]any{
		"body":     "我认为户外市场前景广阔。",
		"mentions": []string{"claude-code"},
	})
	if code != 201 {
		t.Fatalf("muse group send: %d %v", code, res)
	}
	museMsgID := res["id"].(string)

	// 6. Now group has 2 messages.
	code, _, arr = e.do(t, "claude-code", "GET", "/v1/groups/"+groupID+"/messages", nil)
	if code != 200 || len(arr) != 2 {
		t.Fatalf("messages after send: %d, count=%d", code, len(arr))
	}

	// 7. Reply to a specific message (thread).
	code, res, _ = e.do(t, "owner", "POST", "/v1/groups/"+groupID+"/messages", map[string]any{
		"body":     "同意 muse 的观点。",
		"reply_to": museMsgID,
	})
	if code != 201 {
		t.Fatalf("owner reply: %d %v", code, res)
	}

	// 8. Cross-group reply_to is rejected.
	code, _, _ = e.do(t, "owner", "POST", "/v1/groups/"+groupID+"/messages", map[string]any{
		"body":     "test",
		"reply_to": "nonexistent",
	})
	if code != 400 {
		t.Fatalf("cross-group reply should fail: %d", code)
	}

	// 9. Ack works for group messages.
	e.do(t, "muse", "POST", "/v1/groups/"+groupID+"/messages/"+rootMsgID+"/ack", nil)
	code, _, arr = e.do(t, "muse", "GET", "/v1/groups/"+groupID+"/messages", nil)
	if code != 200 {
		t.Fatalf("read after ack: %d", code)
	}
	firstMsg := arr[0].(map[string]any)
	if firstMsg["read"] != true {
		t.Fatalf("first message should be read by muse after ack")
	}

	// 10. Closed group does not accept new messages.
	code, _, _ = e.do(t, "claude-code", "POST", "/v1/groups/"+groupID+"/close", nil)
	if code != 200 {
		t.Fatalf("close group: %d", code)
	}
	code, _, _ = e.do(t, "muse", "POST", "/v1/groups/"+groupID+"/messages", map[string]any{
		"body": "should fail",
	})
	if code != 403 {
		t.Fatalf("post to closed group: %d (expected 403)", code)
	}

	// 11. Closed group messages are still readable.
	code, _, arr = e.do(t, "muse", "GET", "/v1/groups/"+groupID+"/messages", nil)
	if code != 200 || len(arr) != 3 {
		t.Fatalf("read closed group: %d, count=%d", code, len(arr))
	}

	// 12. Only creator/owner can close.
	code2, res2, _ := e.do(t, "claude-code", "POST", "/v1/groups", map[string]any{
		"item": "english", "topic": "test2", "members": []string{"assistant-cloud"}, "body": "hi",
	})
	if code2 != 201 {
		t.Fatalf("create group2: %d", code2)
	}
	g2ID := res2["group"].(map[string]any)["id"].(string)
	code, _, _ = e.do(t, "muse", "POST", "/v1/groups/"+g2ID+"/close", nil)
	if code != 403 {
		t.Fatalf("non-creator close should fail: %d", code)
	}
}

func TestWakeupLifecycle(t *testing.T) {
	e := setup(t)

	// 1. A group @ creates a wakeup for the mentioned member.
	code, res, _ := e.do(t, "claude-code", "POST", "/v1/groups", map[string]any{
		"item": "english", "topic": "请给建议", "members": []string{"assistant-cloud"},
		"body": "test", "mentions": []string{"assistant-cloud"},
	})
	if code != 201 {
		t.Fatalf("create ask group: %d %v", code, res)
	}
	groupID := res["group"].(map[string]any)["id"].(string)

	// 2. Recipient can poll and see the wakeup.
	code, _, arr := e.do(t, "muse", "GET", "/v1/wakeups", nil)
	if code != 200 || len(arr) != 1 {
		t.Fatalf("muse poll: %d, count=%d", code, len(arr))
	}
	wakeup := arr[0].(map[string]any)
	wakeupID := wakeup["id"].(string)
	if wakeup["reason"] != "mention" || wakeup["status"] != "pending" {
		t.Fatalf("unexpected wakeup: %v", wakeup)
	}

	// 3. Sender should NOT see a wakeup for themselves.
	code, _, arr = e.do(t, "claude-code", "GET", "/v1/wakeups", nil)
	if len(arr) != 0 {
		t.Fatalf("sender should have 0 wakeups, got %d", len(arr))
	}

	// 4. Claim the wakeup.
	code, res, _ = e.do(t, "muse", "POST", "/v1/wakeups/"+wakeupID+"/claim", map[string]any{
		"consumer": "muse-local",
	})
	if code != 200 {
		t.Fatalf("claim: %d %v", code, res)
	}
	claimToken := res["claim_token"].(string)
	if claimToken == "" {
		t.Fatal("missing claim_token")
	}
	if res["status"] != "processing" {
		t.Fatalf("expected processing, got %v", res["status"])
	}

	// 5. Double claim fails.
	code, _, _ = e.do(t, "muse", "POST", "/v1/wakeups/"+wakeupID+"/claim", map[string]any{
		"consumer": "muse-local-2",
	})
	if code != 409 {
		t.Fatalf("double claim should fail: %d", code)
	}

	// 6. Finish with reply.
	code, res, _ = e.do(t, "muse", "POST", "/v1/wakeups/"+wakeupID+"/finish", map[string]any{
		"claim_token": claimToken,
		"result":      "replied",
		"body":        "我的建议是多跑步。",
	})
	if code != 200 {
		t.Fatalf("finish: %d %v", code, res)
	}
	if res["status"] != "replied" || res["reply_message_id"] == nil || res["reply_message_id"] == "" {
		t.Fatalf("unexpected finish result: %v", res)
	}

	// 7. After finish, no more pending wakeups.
	code, _, arr = e.do(t, "muse", "GET", "/v1/wakeups", nil)
	if len(arr) != 0 {
		t.Fatalf("should have 0 wakeups after finish, got %d", len(arr))
	}

	// 8. The reply lands in the group, so every member reads it there.
	code, _, arr = e.do(t, "claude-code", "GET", "/v1/groups/"+groupID+"/messages", nil)
	if code != 200 || len(arr) != 2 {
		t.Fatalf("group should hold the opening message and the reply: %d, %d", code, len(arr))
	}
	if last := arr[len(arr)-1].(map[string]any); last["kind"] != "reply" || last["from"] != "assistant-cloud" {
		t.Fatalf("reply not attributed: %v", last)
	}
}

func TestWakeupClaimAcceptsEmptyBody(t *testing.T) {
	e := setup(t)

	// A group @ creates a pending wakeup for muse.
	code, _, _ := e.do(t, "claude-code", "POST", "/v1/groups", map[string]any{
		"item": "english", "topic": "请给建议", "members": []string{"assistant-cloud"},
		"body": "test", "mentions": []string{"assistant-cloud"},
	})
	if code != 201 {
		t.Fatalf("create ask group: %d", code)
	}
	_, _, arr := e.do(t, "muse", "GET", "/v1/wakeups", nil)
	if len(arr) != 1 {
		t.Fatalf("expected 1 wakeup, got %d", len(arr))
	}
	wakeupID := arr[0].(map[string]any)["id"].(string)

	// No body at all must still claim (consumer defaults to the caller).
	code, res, _ := e.do(t, "muse", "POST", "/v1/wakeups/"+wakeupID+"/claim", nil)
	if code != 200 {
		t.Fatalf("claim with empty body: %d %v", code, res)
	}
	if res["claim_token"] == "" || res["claim_token"] == nil {
		t.Fatalf("missing claim_token: %v", res)
	}
}

func TestGroupWakeups(t *testing.T) {
	e := setup(t)

	// Create a group.
	code, res, _ := e.do(t, "claude-code", "POST", "/v1/groups", map[string]any{
		"item":    "english",
		"topic":   "test",
		"members": []string{"assistant-cloud", "owner"},
		"body":    "hello",
	})
	if code != 201 {
		t.Fatalf("create group: %d", code)
	}
	groupID := res["group"].(map[string]any)["id"].(string)

	// Send ask with @muse → should create wakeup for muse.
	code, _, _ = e.do(t, "owner", "POST", "/v1/groups/"+groupID+"/messages", map[string]any{
		"body":     "muse 你怎么看？",
		"mentions": []string{"assistant-cloud"},
	})
	if code != 201 {
		t.Fatalf("group ask: %d", code)
	}

	// Muse should see a wakeup.
	_, _, arr := e.do(t, "muse", "GET", "/v1/wakeups", nil)
	if len(arr) != 1 {
		t.Fatalf("muse should have 1 wakeup, got %d", len(arr))
	}
	w := arr[0].(map[string]any)
	if w["reason"] != "mention" || w["group_id"] != groupID {
		t.Fatalf("unexpected wakeup: %v", w)
	}

	// Send note (no @) → should NOT create wakeup.
	code, _, _ = e.do(t, "owner", "POST", "/v1/groups/"+groupID+"/messages", map[string]any{
		"body": "just a note",
		"kind": "note",
	})
	if code != 201 {
		t.Fatalf("group note: %d", code)
	}

	// Owner and claude-code should have 0 wakeups (they were not @'d).
	_, _, arr = e.do(t, "owner", "GET", "/v1/wakeups", nil)
	if len(arr) != 0 {
		t.Fatalf("owner should have 0 wakeups, got %d", len(arr))
	}
	_, _, arr = e.do(t, "claude-code", "GET", "/v1/wakeups", nil)
	if len(arr) != 0 {
		t.Fatalf("claude-code should have 0 wakeups, got %d", len(arr))
	}
}

package server

import (
	"bytes"
	"encoding/json"
	"github.com/ketchupz1999/agents-home-public/internal/db"
	"github.com/ketchupz1999/agents-home-public/internal/protocol"
	"net/http"
	"testing"
)

func TestActionsSourceAuthorizationIdentityAndRetry(t *testing.T) {
	e := setup(t)
	p := map[string]any{"client_id": "one", "item": "english", "title": "practice", "plan_kind": "later", "source_path": "items/english/CONTEXT.md"}
	for _, field := range []string{"created_by", "created_role", "runtime", "status", "owner", "approve", "source_sha"} {
		p[field] = "spoof"
		code, _, _ := e.do(t, "muse", "POST", "/v1/actions", p)
		if code != 400 {
			t.Fatal(field, code)
		}
		delete(p, field)
	}
	code, a, _ := e.do(t, "muse", "POST", "/v1/actions", p)
	if code != 201 || a["status"] != "suggested" || a["created_by"] != "assistant-cloud" || a["source_sha"] == "" {
		t.Fatal(code, a)
	}
	id := a["id"].(string)
	code, _, _ = e.do(t, "muse", "POST", "/v1/actions/"+id+"/complete", nil)
	if code < 400 {
		t.Fatal("agent write enabled")
	}
	planned, err := e.db.UpdateAction(id, db.ActionActor{Name: "owner", Owner: true}, db.ActionUpdate{Operation: "schedule", Revision: 1, PlanKind: "later"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.db.UpdateAction(id, db.ActionActor{Name: "owner", Owner: true}, db.ActionUpdate{Operation: "complete", Revision: planned.Revision})
	if err != nil {
		t.Fatal(err)
	}
	code, a, _ = e.do(t, "muse", "POST", "/v1/actions", p)
	if code != 200 || a["status"] != "done" {
		t.Fatal(code, a)
	}
	p["title"] = "different"
	code, _, _ = e.do(t, "muse", "POST", "/v1/actions", p)
	if code != 409 {
		t.Fatal(code)
	}
	p["client_id"] = "private"
	p["source_path"] = "items/secret/CONTEXT.md"
	code, _, _ = e.do(t, "limited", "POST", "/v1/actions", p)
	if code != 404 {
		t.Fatal(code)
	}
	code, private, _ := e.do(t, "muse", "POST", "/v1/actions", p)
	if code != 201 {
		t.Fatal(code, private)
	}
	code, _, list := e.do(t, "limited", "GET", "/v1/actions", nil)
	if code != 200 || len(list) != 1 {
		t.Fatal(code, list)
	}
	code, _, _ = e.do(t, "limited", "GET", "/v1/actions/"+private["id"].(string), nil)
	if code != 404 {
		t.Fatal(code)
	}
	code, detail, _ := e.do(t, "limited", "GET", "/v1/actions/"+id, nil)
	if code != 200 || len(detail["history"].([]any)) != 3 {
		t.Fatal(code, detail)
	}
	e.db.PutMessage(protocol.Message{ID: "source", Item: "secret"})
	delete(p, "source_path")
	p["source_message_id"] = "source"
	p["client_id"] = "message"
	code, _, _ = e.do(t, "muse", "POST", "/v1/actions", p)
	if code != 404 {
		t.Fatal("cross item source", code)
	}
	p["source_message_id"] = "missing"
	code, _, _ = e.do(t, "muse", "POST", "/v1/actions", p)
	if code != 404 {
		t.Fatal(code)
	}
	p["source_path"] = "items/english/CONTEXT.md"
	code, _, _ = e.do(t, "muse", "POST", "/v1/actions", p)
	if code != 400 {
		t.Fatal(code)
	}
}

func TestActionRetryAfterSourceRemoved(t *testing.T) {
	e := setup(t)
	p := map[string]any{"client_id": "removed-source", "item": "english", "title": "practice", "plan_kind": "later", "source_path": "items/english/CONTEXT.md"}
	code, a, _ := e.do(t, "muse", "POST", "/v1/actions", p)
	if code != 201 {
		t.Fatal(code, a)
	}
	git(t, e.seed, "rm", "items/english/CONTEXT.md")
	git(t, e.seed, "commit", "-m", "remove source")
	git(t, e.seed, "push", "origin", "main")
	e.do(t, "muse", "GET", "/v1/whoami", nil)
	code, retry, _ := e.do(t, "muse", "POST", "/v1/actions", p)
	if code != 200 || retry["id"] != a["id"] || retry["source_sha"] != a["source_sha"] {
		t.Fatal(code, retry)
	}
}

func TestActionRuntimeUsesAuthenticatedRun(t *testing.T) {
	e := setup(t)
	code, _, _ := e.do(t, "claude-code", "POST", "/v1/runs", map[string]string{"client_run_id": "action_run_123", "harness": "codex"})
	if code != 200 {
		t.Fatal(code)
	}
	for _, key := range []string{"claude-code", "muse"} {
		body := []byte(`{"client_id":"runtime","item":"english","title":"practice","plan_kind":"later","source_path":"items/english/CONTEXT.md"}`)
		req, _ := http.NewRequest("POST", e.ts.URL+"/v1/actions", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+e.keys[key])
		req.Header.Set("X-ASpace-Run-ID", "action_run_123")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if key == "muse" {
			resp.Body.Close()
			if resp.StatusCode != 404 {
				t.Fatal(resp.StatusCode)
			}
			continue
		}
		var a db.Action
		err = json.NewDecoder(resp.Body).Decode(&a)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 201 || a.Runtime == nil || a.Runtime.Harness != "codex" || a.Runtime.RunID != "action_run_123" || a.CreatedBy != "claude-code" {
			t.Fatal(a, resp.StatusCode, err)
		}
	}
}

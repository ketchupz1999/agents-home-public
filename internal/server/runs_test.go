package server

import (
	"bytes"
	"encoding/json"
	"github.com/ketchupz1999/agents-home-public/internal/db"
	"github.com/ketchupz1999/agents-home-public/internal/store"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRunReferenceAndMessageSnapshot(t *testing.T) {
	e := setup(t)
	in := map[string]string{"client_run_id": "test_run_123", "harness": "codex"}
	for i := 0; i < 2; i++ {
		code, _, _ := e.do(t, "claude-code", "POST", "/v1/runs", in)
		if code != 200 {
			t.Fatal(code)
		}
	}
	in["harness"] = "other"
	code, _, _ := e.do(t, "claude-code", "POST", "/v1/runs", in)
	if code != 409 {
		t.Fatal(code)
	}
	code, _, _ = e.do(t, "muse", "GET", "/v1/runs/test_run_123", nil)
	if code != 404 {
		t.Fatal(code)
	}
	for _, tc := range []struct {
		run  string
		want int
	}{{"test_run_123", 201}, {"other_run_123", 404}, {"bad", 400}} {
		body := []byte(`{"topic":"hello","item":"english","members":["assistant-cloud"],"body":"hi","mentions":["assistant-cloud"]}`)
		req, _ := http.NewRequest("POST", e.ts.URL+"/v1/groups", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+e.keys["claude-code"])
		req.Header.Set("X-ASpace-Run-ID", tc.run)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatal(resp.StatusCode, string(raw))
		}
		if tc.want == 201 {
			var result struct {
				RootMessageID string `json:"root_message_id"`
			}
			json.Unmarshal(raw, &result)
			m, err := e.db.GetMessage(result.RootMessageID)
			// The client cannot forge a role through the body; the server
			// stamps it from the registry (empty here: no role declared).
			if err != nil || m.FromRole != "" || m.From != "claude-code" {
				t.Fatal(m, err)
			}
		}
	}
}
func TestItemObservationsAndFailureDegrade(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "items/test"), 0700)
	d, err := db.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := &Server{Store: &store.Store{Dir: dir}, DB: d}
	key := &Key{Name: "agent", Owner: true}
	get := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/v1/items/test", nil)
		r.SetPathValue("id", "test")
		w := httptest.NewRecorder()
		s.getItem(w, r, key)
		return w
	}
	if w := get(); w.Code != 200 {
		t.Fatal(w.Code)
	}
	rows, _ := d.ListObservations(db.ObservationFilter{})
	if len(rows) != 0 {
		t.Fatal("missing context recorded")
	}
	os.WriteFile(filepath.Join(dir, "items/test/CONTEXT.md"), []byte{}, 0600)
	get()
	rows, _ = d.ListObservations(db.ObservationFilter{})
	if len(rows) != 1 || rows[0].ContentSHA != store.BlobSHA([]byte{}) {
		t.Fatal(rows)
	}
	d.Exec(`CREATE TRIGGER deny_observation BEFORE INSERT ON agent_observations BEGIN SELECT RAISE(ABORT,'no'); END`)
	w := get()
	if w.Code != 200 || w.Header().Get("X-ASpace-Observation") != "unavailable" {
		t.Fatal(w.Code, w.Header())
	}
	os.Remove(filepath.Join(dir, "items/test/CONTEXT.md"))
	os.Mkdir(filepath.Join(dir, "items/test/CONTEXT.md"), 0700)
	get()
	rows, _ = d.ListObservations(db.ObservationFilter{})
	if len(rows) != 1 {
		t.Fatal("read error recorded")
	}
}

func TestPublishedMappingAndUnavailableRejectMessages(t *testing.T) {
	e := setup(t)
	os.MkdirAll(filepath.Join(e.seed, "agents/team"), 0700)
	os.WriteFile(filepath.Join(e.seed, "agents/registry.yaml"), []byte("roles:\n - id: team\n   character: codex\nagents:\n - id: claude-code\n   role_id: team\n"), 0600)
	os.WriteFile(filepath.Join(e.seed, "agents/team/card.md"), []byte("---\ndisplay_name: Team\n---\n\n"), 0600)
	git(t, e.seed, "add", ".")
	git(t, e.seed, "commit", "-qm", "published mapping")
	git(t, e.seed, "push", "-q", "origin", "main")
	e.do(t, "claude-code", "GET", "/v1/whoami", nil)
	code, res, _ := e.do(t, "claude-code", "POST", "/v1/groups", map[string]any{"topic": "mapping", "item": "english", "members": []string{"assistant-cloud"}, "body": "hi", "from_role": "forged"})
	if code != 201 {
		t.Fatal(code, res)
	}
	m, err := e.db.GetMessage(res["root_message_id"].(string))
	if err != nil || m.FromRole != "team" {
		t.Fatal(m, err)
	}
	os.WriteFile(filepath.Join(e.seed, "agents/registry.yaml"), []byte("roles: invalid"), 0600)
	git(t, e.seed, "commit", "-qam", "broken mapping")
	git(t, e.seed, "push", "-q", "origin", "main")
	e.do(t, "claude-code", "GET", "/v1/whoami", nil)
	code, _, _ = e.do(t, "claude-code", "POST", "/v1/groups", map[string]string{"item": "english", "topic": "must fail", "body": "x"})
	if code != 400 {
		t.Fatal(code)
	}
	var count int
	e.db.QueryRow(`SELECT count(*) FROM messages`).Scan(&count)
	if count != 1 {
		t.Fatal("broken mapping wrote message")
	}
}

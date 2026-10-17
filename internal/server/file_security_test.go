package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOverviewAndItemRespectFileScopes(t *testing.T) {
	e := setup(t)
	req, _ := http.NewRequest("GET", e.ts.URL+"/v1/now", nil)
	req.Header.Set("Authorization", "Bearer "+e.keys["limited"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(b), "not for muse") {
		t.Error("overview leaks inaccessible title")
	}
	root := filepath.Join(filepath.Dir(e.seed), "srv")
	context := filepath.Join(root, "items/english/CONTEXT.md")
	if err := os.Remove(context); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../secret/CONTEXT.md", context); err != nil {
		t.Fatal(err)
	}
	_, obj, _ := e.do(t, "limited", "GET", "/v1/items/english", nil)
	encoded, _ := json.Marshal(obj)
	if strings.Contains(string(encoded), "not for muse") {
		t.Error("item endpoint follows unauthorized alias")
	}
}

func TestFileAndSearchDoNotFollowUnauthorizedLinks(t *testing.T) {
	e := setup(t)
	root := filepath.Join(filepath.Dir(e.seed), "srv")
	outside := filepath.Join(filepath.Dir(e.seed), "outside.txt")
	if err := os.WriteFile(outside, []byte("OUTSIDEAUDITMARKER"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"outside.md": outside, "private.md": "../items/secret/CONTEXT.md", "good.md": "preferences.md", "git.md": "../.git/config"} {
		if err := os.Symlink(target, filepath.Join(root, "profile", name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"profile/outside.md", "profile/private.md", "profile/git.md"} {
		code, _, _ := e.do(t, "limited", "GET", "/v1/file?path="+path, nil)
		if code != 404 {
			t.Errorf("%s returned %d, want 404", path, code)
		}
	}
	code, obj, _ := e.do(t, "limited", "GET", "/v1/file?path=profile/good.md", nil)
	if code != 200 || obj["content"] != "# 偏好\n" {
		t.Errorf("permitted link status %d", code)
	}
	for _, q := range []string{"OUTSIDEAUDITMARKER", "not%20for%20muse"} {
		code, obj, _ := e.do(t, "limited", "GET", "/v1/search?q="+q, nil)
		b, _ := json.Marshal(obj["hits"])
		if code != 200 || strings.Contains(string(b), "outside.md") || strings.Contains(string(b), "private.md") {
			t.Errorf("search leaked symlink: %s", b)
		}
	}
}

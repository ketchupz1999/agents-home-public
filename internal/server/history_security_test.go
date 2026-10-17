package server

import "testing"

func TestRestrictedKeyCannotReadGlobalGitHistory(t *testing.T) {
	e := setup(t)
	code, _, _ := e.do(t, "limited", "GET", "/v1/log", nil)
	if code != 403 {
		t.Fatalf("restricted key read global commit metadata: %d", code)
	}
	for _, caller := range []string{"owner", "muse"} {
		code, _, _ := e.do(t, caller, "GET", "/v1/log", nil)
		if code != 200 {
			t.Errorf("full-repository reader %s denied: %d", caller, code)
		}
	}
}

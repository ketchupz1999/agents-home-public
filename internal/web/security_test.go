package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionsExpireAndLogoutRevokes(t *testing.T) {
	w := testThreadWeb(t)
	original := w.Now()
	token := w.token()
	call := func(path string) int {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(&http.Cookie{Name: cookieName, Value: token})
		out := httptest.NewRecorder()
		w.Handler().ServeHTTP(out, r)
		return out.Code
	}
	if call("/d/") != 200 {
		t.Fatal("fresh session rejected")
	}
	call("/d/logout")
	if call("/d/") != 303 {
		t.Error("logged out session replayed")
	}
	token = w.token()
	w.Now = func() time.Time { return original.Add(31 * 24 * time.Hour) }
	if call("/d/") != 303 {
		t.Error("expired session replayed")
	}
}

func TestCrossOriginDashboardWriteRejected(t *testing.T) {
	w := testThreadWeb(t)
	req := httptest.NewRequest("POST", "https://space.example.test/d/branch/agent/a/b/reject", nil)
	req.Header.Set("Origin", "https://evil.example.test")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: w.token()})
	out := httptest.NewRecorder()
	w.Handler().ServeHTTP(out, req)
	if out.Code != 403 {
		t.Fatalf("same-site cross-origin POST returned %d", out.Code)
	}
}

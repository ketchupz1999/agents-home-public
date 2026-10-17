package server

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestLogsBoundProxyTrustAndOmitSecrets(t *testing.T) {
	for _, tc := range []struct{ peer, forwarded, want string }{
		{"127.0.0.1:1234", "198.51.100.1, 203.0.113.7", "203.0.113.7"},
		{"198.51.100.8:1234", "203.0.113.7", "198.51.100.8"},
	} {
		var buf bytes.Buffer
		old := log.Writer()
		log.SetOutput(&buf)
		req := httptest.NewRequest("GET", "/v1/search?q=PRIVATEQUERY", nil)
		req.RemoteAddr = tc.peer
		req.Header.Set("X-Forwarded-For", tc.forwarded)
		req.Header.Set("Authorization", "Bearer PRIVATETOKEN")
		logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) })).ServeHTTP(httptest.NewRecorder(), req)
		log.SetOutput(old)
		line := buf.String()
		if !strings.Contains(line, "ip="+tc.want) {
			t.Errorf("missing verified client IP: %s", line)
		}
		if strings.Contains(line, "PRIVATE") {
			t.Errorf("secret query or credential logged")
		}
	}
}

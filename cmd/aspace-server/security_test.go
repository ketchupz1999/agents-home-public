package main

import (
	"net/http"
	"testing"
	"time"
)

func TestServerBoundsSlowRequestsWithoutBreakingLongPoll(t *testing.T) {
	s := newHTTPServer("127.0.0.1:0", http.NewServeMux())
	if s.ReadTimeout <= 0 || s.IdleTimeout <= 0 || s.WriteTimeout <= 60*time.Second || s.MaxHeaderBytes <= 0 {
		t.Fatalf("unbounded requests: read=%s idle=%s write=%s headers=%d", s.ReadTimeout, s.IdleTimeout, s.WriteTimeout, s.MaxHeaderBytes)
	}
}

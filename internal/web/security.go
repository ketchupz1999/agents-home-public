package web

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"
)

const sessionLifetime = 12 * time.Hour

// Sessions are random, server-side and revocable. Restarting the process
// invalidates existing sessions; no owner key or deterministic bearer is sent.
func (w *Web) token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	token := hex.EncodeToString(b)
	w.sessionMu.Lock()
	defer w.sessionMu.Unlock()
	if w.sessions == nil {
		w.sessions = map[string]time.Time{}
	}
	now := w.Now()
	for id, expiry := range w.sessions {
		if !now.Before(expiry) {
			delete(w.sessions, id)
		}
	}
	// Bound memory even if a legitimate key holder repeatedly logs in.
	if len(w.sessions) >= 128 {
		for id := range w.sessions {
			delete(w.sessions, id)
			break
		}
	}
	w.sessions[token] = now.Add(sessionLifetime)
	return token
}

func (w *Web) validSession(token string) bool {
	if token == "" {
		return false
	}
	w.sessionMu.Lock()
	defer w.sessionMu.Unlock()
	expiry, ok := w.sessions[token]
	if !ok || !w.Now().Before(expiry) {
		delete(w.sessions, token)
		return false
	}
	return true
}

func (w *Web) revokeSession(r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		w.sessionMu.Lock()
		delete(w.sessions, c.Value)
		w.sessionMu.Unlock()
	}
}

// CrossOriginProtection rejects browser cross-origin writes, including
// attacks from sibling subdomains. Every dashboard route shares this guard.
func (w *Web) protect(next http.Handler) http.Handler {
	sameOrigin := http.NewCrossOriginProtection().Handler(next)
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", "no-store")
		rw.Header().Set("X-Content-Type-Options", "nosniff")
		rw.Header().Set("X-Frame-Options", "DENY")
		rw.Header().Set("Referrer-Policy", "no-referrer")
		sameOrigin.ServeHTTP(rw, r)
	})
}

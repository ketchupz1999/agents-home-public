package web

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func identityFile(t *testing.T, w *Web, p string, raw []byte) {
	t.Helper()
	p = filepath.Join(w.Store.Dir, p)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, raw, 0600); err != nil {
		t.Fatal(err)
	}
	rel, _ := filepath.Rel(w.Store.Dir, p)
	if rel == "agents/registry.yaml" || strings.HasSuffix(rel, "/card.md") {
		webTestGit(t, w.Store.Dir, "add", "--", rel)
		webTestGit(t, w.Store.Dir, "commit", "--allow-empty", "-qm", "fixture update")
	}
}
func avatarCard(t *testing.T, w *Web, p string) {
	t.Helper()
	identityFile(t, w, "agents/picture/card.md", []byte("---\navatar_image: '"+p+"'\n---\n"))
}
func TestAvatarSecurityAndCaching(t *testing.T) {
	w := testThreadWeb(t)
	var pngBytes bytes.Buffer
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.White)
	png.Encode(&pngBytes, img)
	// A real tiny lossless WebP, decoded by the same supported image package.
	webpBytes, err := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name string
		raw  []byte
	}{{"png", pngBytes.Bytes()}, {"webp", webpBytes}} {
		t.Run(fixture.name, func(t *testing.T) {
			p := "agents/_assets/avatars/a." + fixture.name
			identityFile(t, w, p, fixture.raw)
			avatarCard(t, w, p)
			if got := requestThread(t, w, "GET", "/d/avatar/picture", nil, false); got.Code != http.StatusSeeOther {
				t.Fatal("unauthenticated image exposed")
			}
			got := requestThread(t, w, "GET", "/d/avatar/picture", nil, true)
			if got.Code != 200 {
				t.Fatalf("valid image rejected: %d", got.Code)
			}
			if !bytes.Equal(got.Body.Bytes(), fixture.raw) || got.Header().Get("Content-Type") != "image/"+fixture.name || !strings.Contains(got.Header().Get("Cache-Control"), "private") || got.Header().Get("ETag") == "" {
				t.Fatal("bad image response")
			}
			req := httptestAvatarRequest(w, got.Header().Get("ETag"))
			if req != 304 {
				t.Fatalf("etag status %d", req)
			}
		})
	}
	identityFile(t, w, "agents/_assets/avatars/fake.png", []byte("not a PNG"))
	identityFile(t, w, "agents/_assets/avatars/large.png", make([]byte, maxAvatar+1))
	identityFile(t, w, "agents/_assets/avatars/truncated.png", pngBytes.Bytes()[:33])
	outside := filepath.Join(t.TempDir(), "secret.png")
	os.WriteFile(outside, pngBytes.Bytes(), 0600)
	os.Symlink(outside, filepath.Join(w.Store.Dir, "agents/_assets/avatars/link.png"))
	for _, p := range []string{"https://example.com/image.png", "/tmp/a.png", "agents/_assets/avatars/../../secret.png", "agents/_assets/avatars/link.png", "agents/_assets/avatars/fake.png", "agents/_assets/avatars/large.png", "agents/_assets/avatars/truncated.png", "profile/private.png"} {
		avatarCard(t, w, p)
		got := requestThread(t, w, "GET", "/d/avatar/picture", nil, true)
		if got.Code != 404 {
			t.Fatalf("unsafe image %s: %d", p, got.Code)
		}
	}
}
func TestRegistryAndHistoricalIdentity(t *testing.T) {
	w := testThreadWeb(t)
	identityFile(t, w, "agents/registry.yaml", []byte("agents:\n  - id: workmac-codex\n    environment: workmac\n    agent: Codex\n    status: active\n"))
	identityFile(t, w, "agents/workmac-codex/card.md", []byte("---\ndisplay_name: '<new>'\nbio: '<script>bio</script>'\n---\n"))
	got := requestThread(t, w, "GET", "/d/agents", nil, true)
	body := got.Body.String()
	if got.Code != 200 || !strings.Contains(body, "workmac-codex") || !strings.Contains(body, "&lt;new&gt;") || strings.Contains(body, "<script>bio") {
		t.Fatal("directory missing unsent agent or unsafe HTML")
	}
	if defaultIdentity("owner").Color == defaultIdentity("assistant-cloud").Color {
		t.Fatal("owner color collision")
	}
	if !strings.Contains(identityLabel(w.identity("claude-local")), "claude-local") {
		t.Fatal("historical ID lost")
	}
	if !strings.Contains(identityBadge(w.identity("unknown-full-agent-id")), "unknown-full-agent-id") {
		t.Fatal("unknown ID lost")
	}
	identityFile(t, w, "agents/registry.yaml", []byte("agents:\n - id: same\n - id: same\n"))
	if _, err := w.registry(); err == nil {
		t.Fatal("duplicate accepted")
	}
	identityFile(t, w, "agents/registry.yaml", []byte("agents:\n - id: ../bad\n"))
	if _, err := w.registry(); err == nil {
		t.Fatal("unsafe ID accepted")
	}
}

func httptestAvatarRequest(w *Web, tag string) int {
	r := httptest.NewRequest("GET", "/d/avatar/picture", nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: w.token()})
	r.Header.Set("If-None-Match", tag)
	rw := httptest.NewRecorder()
	w.Handler().ServeHTTP(rw, r)
	return rw.Code
}

func TestCropBoundsAndFallback(t *testing.T) {
	w := testThreadWeb(t)
	var raw bytes.Buffer
	png.Encode(&raw, image.NewNRGBA(image.Rect(0, 0, 4, 4)))
	identityFile(t, w, "agents/picture/a.png", raw.Bytes())
	avatarCard(t, w, "agents/picture/a.png")
	v := w.identity("picture")
	v.Crop = &avatarCrop{X: 1, Y: 1, Width: 2, Height: 2}
	if _, _, err := w.readAvatar(v); err != nil {
		t.Fatal(err)
	}
	markup := avatar(v)
	if !strings.Contains(markup, `viewBox="1 1 2 2"`) || !strings.Contains(markup, `this.parentNode.remove()`) {
		t.Fatal("crop or fallback missing")
	}
	v.Crop = &avatarCrop{X: 3, Y: 1, Width: 2, Height: 2}
	if _, _, err := w.readAvatar(v); err == nil {
		t.Fatal("out-of-image crop accepted")
	}
	if validCrop(avatarCrop{X: int(^uint(0) >> 1), Y: 0, Width: 2, Height: 2}) {
		t.Fatal("overflowing crop accepted")
	}
}

func TestAvatarRootDirectoryCannotRedirect(t *testing.T) {
	for _, base := range []string{"agents/_assets/avatars", "agents/picture"} {
		t.Run(base, func(t *testing.T) {
			w := testThreadWeb(t)
			var data bytes.Buffer
			_ = png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 2, 2)))
			identityFile(t, w, "profile/private.png", data.Bytes())
			if err := os.MkdirAll(filepath.Dir(filepath.Join(w.Store.Dir, base)), 0700); err != nil {
				t.Fatal(err)
			}
			target := "../../profile"
			if base == "agents/picture" {
				target = "../profile"
			}
			if err := os.Symlink(target, filepath.Join(w.Store.Dir, base)); err != nil {
				t.Fatal(err)
			}
			v := defaultIdentity("picture")
			v.Image = base + "/private.png"
			if _, _, err := w.readAvatar(v); err == nil {
				t.Fatal("allowed directory symlink exposed profile file")
			}
		})
	}
}

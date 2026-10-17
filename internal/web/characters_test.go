package web

import (
	"bytes"
	"encoding/json"
	"image"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCharacterAssetMatchesManifestAndRequiresSession(t *testing.T) {
	for kind, bundle := range characterBundles {
		t.Run(kind, func(t *testing.T) {
			w := testThreadWeb(t)
			var manifest struct {
				Atlas       string
				FrameWidth  int
				FrameHeight int
				Columns     int
				Rows        int
				Animations  map[string]struct {
					Row              int
					Frames           int
					FrameDurationsMs []int
				}
			}
			if err := json.Unmarshal([]byte(bundle.Manifest), &manifest); err != nil {
				t.Fatal(err)
			}
			if got := requestThread(t, w, "GET", manifest.Atlas, nil, false); got.Code != http.StatusSeeOther {
				t.Fatal("character image bypassed dashboard authentication")
			}
			got := requestThread(t, w, "GET", manifest.Atlas, nil, true)
			if got.Code != http.StatusOK || !strings.Contains(got.Header().Get("Cache-Control"), "private") {
				t.Fatalf("bad character image response: %d", got.Code)
			}
			decoded, format, err := image.Decode(bytes.NewReader(got.Body.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			if got.Header().Get("Content-Type") != "image/"+format {
				t.Fatal("character content type does not match image encoding")
			}
			if decoded.Bounds().Dx() != manifest.Columns*manifest.FrameWidth || decoded.Bounds().Dy() != manifest.Rows*manifest.FrameHeight {
				t.Fatal("sprite grid no longer matches manifest")
			}
			for _, name := range []string{"idle", "working", "waiting", "complete"} {
				animation, ok := manifest.Animations[name]
				if !ok || animation.Row < 0 || animation.Row >= manifest.Rows || animation.Frames <= 0 || animation.Frames > manifest.Columns || len(animation.FrameDurationsMs) != animation.Frames {
					t.Fatalf("invalid animation: %s", name)
				}
				for _, duration := range animation.FrameDurationsMs {
					if duration <= 0 {
						t.Fatalf("nonpositive duration can stall the player: %s", name)
					}
				}
			}
			tag := got.Header().Get("ETag")
			if tag == "" {
				t.Fatal("missing cache validator")
			}
			req := httptest.NewRequest("GET", manifest.Atlas, nil)
			req.AddCookie(&http.Cookie{Name: cookieName, Value: w.token()})
			req.Header.Set("If-None-Match", tag)
			rw := httptest.NewRecorder()
			w.Handler().ServeHTTP(rw, req)
			if rw.Code != http.StatusNotModified || rw.Body.Len() != 0 {
				t.Fatal("unchanged sprite should not be resent")
			}
		})
	}
}

func TestCharactersFollowTypeAndExcludeOwnerAndUnknown(t *testing.T) {
	w := testThreadWeb(t)
	for _, tc := range []struct {
		id, kind, status string
		want             bool
	}{
		{"assistant-cloud", "Muse", "active", true},
		{"workmac-claude", "Claude Code", "active", true},
		{"claude-local", "Claude Code", "", true},
		{"workmac-codex", "Codex", "active", true},
		{"laptop-codex", "Codex", "active", true},
		{"chatgpt", "ChatGPT", "active", true},
		{"owner", "Muse", "active", false},
		{"unknown", "Other", "active", false},
	} {
		identityFile(t, w, "agents/registry.yaml", []byte("agents:\n  - id: "+tc.id+"\n    agent: "+tc.kind+"\n    status: "+tc.status+"\n"))
		got := requestThread(t, w, "GET", "/d/agents/"+tc.id, nil, true)
		body := got.Body.String()
		if strings.Contains(body, `class="agent-character"`) != tc.want {
			t.Fatalf("unexpected player for %s/%s", tc.id, tc.status)
		}
		if tc.want && (!strings.Contains(body, "非实时状态") || !strings.Contains(body, `class="character-fallback"`)) {
			t.Fatal("preview disclosure or static fallback missing")
		}
	}
}

func TestCharacterAssetsOnlyExposeAllowlistedImages(t *testing.T) {
	w := testThreadWeb(t)
	for _, name := range []string{"muse.json", "player.js", "player.css", "muse-prompt.txt", "missing.png"} {
		if got := requestThread(t, w, "GET", "/d/characters/"+name, nil, true); got.Code != http.StatusNotFound {
			t.Fatalf("unexpected asset exposed: %s (%d)", name, got.Code)
		}
	}
}

func TestMultipleCharactersKeepNamesEscapedAndLoadPlayerOnce(t *testing.T) {
	w := testThreadWeb(t)
	identityFile(t, w, "agents/registry.yaml", []byte("agents:\n  - id: workmac-codex\n    agent: Codex\n  - id: laptop-codex\n    agent: Codex\n  - id: chatgpt\n    agent: ChatGPT\n"))
	identityFile(t, w, "agents/laptop-codex/card.md", []byte("---\ndisplay_name: 'Codex <script>alert(1)</script>'\n---\n"))
	body := requestThread(t, w, "GET", "/d/agents", nil, true).Body.String()
	if strings.Count(body, `data-character="codex"`) != 2 || strings.Count(body, `data-character="chatgpt"`) != 1 {
		t.Fatal("same kind on different devices did not share the character")
	}
	if strings.Contains(body, "<script>alert(1)") || !strings.Contains(body, `Codex &lt;script&gt;`) {
		t.Fatal("display name escaped incorrectly")
	}
	if strings.Count(body, `const page=document.querySelector('.team-page')`) != 1 {
		t.Fatal("player must be included once per page")
	}
}

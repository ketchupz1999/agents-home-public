package web

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"net/http"
	"time"
)

// Characters are bundled with the dashboard. No agent-controlled asset
// paths or activity reporting are introduced by this visual-only preview.
//
//go:embed characters/muse-v1.png
var museSprites []byte

//go:embed characters/muse.json
var museManifest string

//go:embed characters/claude-v1.png
var claudeSprites []byte

//go:embed characters/claude.json
var claudeManifest string

//go:embed characters/codex-v4.webp
var codexSprites []byte

//go:embed characters/codex.json
var codexManifest string

//go:embed characters/chatgpt-v1.png
var chatgptSprites []byte

//go:embed characters/chatgpt.json
var chatgptManifest string

//go:embed characters/player.css
var characterCSS string

//go:embed characters/player.js
var characterJS string

type characterBundle struct {
	ID       string
	Manifest string
}

// Registry agent types choose the artwork; device-specific IDs keep their own
// identity and controls while sharing the same character bundle.
var characterBundles = map[string]characterBundle{
	"Muse":        {ID: "muse", Manifest: museManifest},
	"Claude Code": {ID: "claude", Manifest: claudeManifest},
	"Codex":       {ID: "codex", Manifest: codexManifest},
	"ChatGPT":     {ID: "chatgpt", Manifest: chatgptManifest},
}

type characterAsset struct {
	Data []byte
	MIME string
	ETag string
}

func newCharacterAsset(data []byte, mime string) characterAsset {
	return characterAsset{Data: data, MIME: mime, ETag: fmt.Sprintf(`"%x"`, sha256.Sum256(data))}
}

// Only these exact image names are served. Manifests, prompts and arbitrary
// paths never enter the file handler.
var characterAssets = map[string]characterAsset{
	"muse-v1.png":    newCharacterAsset(museSprites, "image/png"),
	"claude-v1.png":  newCharacterAsset(claudeSprites, "image/png"),
	"codex-v4.webp":  newCharacterAsset(codexSprites, "image/webp"),
	"chatgpt-v1.png": newCharacterAsset(chatgptSprites, "image/png"),
}

func characterImage(rw http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	asset, ok := characterAssets[name]
	if !ok {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Content-Type", asset.MIME)
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.Header().Set("Cache-Control", "private, max-age=300")
	rw.Header().Set("ETag", asset.ETag)
	http.ServeContent(rw, r, name, time.Time{}, bytes.NewReader(asset.Data))
}

func characterPreview(v agentIdentity) string {
	bundle, ok := teamBundle(v)
	if !ok || v.ID == "owner" {
		return ""
	}
	return fmt.Sprintf(`<section class="agent-character" data-manifest="%[1]s" data-name="%[2]s" data-character="%[4]s" aria-label="%[2]s 形象预览">
  <div class="character-title"><span>小小日常</span><span class="muted">形象预览 · 非实时状态</span></div>
  <div class="character-scene">
    <div class="character-portrait">
      <span class="character-fallback">%[3]s</span>
      <canvas width="256" height="256" role="img" aria-label="%[2]s：放空一下" hidden></canvas>
    </div>
    <div class="character-menu">
      <p class="character-caption" aria-live="polite">点选动作，看看我的小习惯。</p>
      <div class="character-controls" hidden>
        <div class="character-actions" role="group" aria-label="选择 %[2]s 的预览动作">
          <button type="button" data-action="idle" aria-pressed="true">放空一下</button>
          <button type="button" data-action="working" aria-pressed="false">认真开工</button>
          <button type="button" data-action="waiting" aria-pressed="false">等你拿主意</button>
          <button type="button" data-action="complete" aria-pressed="false">做好啦</button>
        </div>
        <button type="button" class="character-pause" aria-label="暂停 %[2]s 动画">暂停动画</button>
      </div>
    </div>
  </div>
</section>`, esc(bundle.Manifest), esc(v.Name), avatar(v), esc(bundle.ID))
}

func characterPlayer() string {
	return "<style>" + characterCSS + "</style><script>" + characterJS + "</script>"
}

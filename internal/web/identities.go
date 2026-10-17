package web

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"hash/fnv"
	"image"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ketchupz1999/agents-home-public/internal/agents"
	"github.com/ketchupz1999/agents-home-public/internal/protocol"
	_ "golang.org/x/image/webp"
)

type agentRegistration struct {
	ID          string `yaml:"id"`
	Environment string `yaml:"environment"`
	Agent       string `yaml:"agent"`
}
type avatarCrop struct {
	X      int `yaml:"x"`
	Y      int `yaml:"y"`
	Width  int `yaml:"width"`
	Height int `yaml:"height"`
}
type agentIdentity struct {
	ID          string      `yaml:"-"`
	Name        string      `yaml:"display_name"`
	Text        string      `yaml:"avatar_text"`
	Color       string      `yaml:"avatar_color"`
	Image       string      `yaml:"avatar_image"`
	Crop        *avatarCrop `yaml:"avatar_crop"`
	Bio         string      `yaml:"bio"`
	Environment string      `yaml:"-"`
	Kind        string      `yaml:"-"`
	RoleID      string      `yaml:"-"`
	Character   string      `yaml:"-"`
	AvatarID    string      `yaml:"-"`
}

var avatarColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func defaultIdentity(id string) agentIdentity {
	known := map[string]agentIdentity{"owner": {Name: "本人", Text: "我"}, "assistant-cloud": {Name: "狗腿子（Muse）", Text: "狗"}, "claude-local": {Name: "Claude Code", Text: "C"}, "codex-local": {Name: "Codex", Text: "C"}, "opencode-local": {Name: "opencode", Text: "O"}}
	v, ok := known[id]
	if !ok {
		first, _ := utf8.DecodeRuneInString(id)
		v = agentIdentity{Name: id, Text: strings.ToUpper(string(first))}
		if id == "" {
			v.Name = "未知 Agent"
			v.Text = "?"
		}
	}
	palette := []string{"#466D59", "#775A40", "#536B95", "#7B5478", "#3C727B", "#8A5650"}
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	v.Color = palette[int(h.Sum32())%len(palette)]
	if id == "owner" {
		v.Color = "#505653"
	}
	v.ID = id
	return v
}

func (w *Web) registry() ([]agentRegistration, error) {
	reg, err := agents.Load(w.Store)
	if err != nil {
		return nil, err
	}
	out := []agentRegistration{}
	for _, a := range reg.Agents {
		out = append(out, agentRegistration{ID: a.ID, Environment: a.Environment, Agent: a.Agent})
	}
	return out, nil
}

// identityAt 将同一已发布快照的角色显示信息应用到原身份，不改变其投递身份。
func (w *Web) identityAt(reg *agents.Registry, id, explicitRole string) agentIdentity {
	v := defaultIdentity(id)
	if reg == nil || !protocol.ValidName(id) {
		return v
	}
	cardID := id
	roleID := explicitRole
	if roleID == "" {
		roleID = reg.RoleFor(id)
	}
	for _, a := range reg.Agents {
		if a.ID == id {
			v.Environment = a.Environment
			v.Kind = a.Agent
			break
		}
	}
	for _, role := range reg.Roles {
		if role.ID == roleID {
			v.RoleID = roleID
			v.Character = role.Character
			v.Kind = ""
			cardID = roleID
			break
		}
	}
	c, ok := reg.Cards[cardID]
	if !ok && cardID == id {
		if raw, err := w.Store.ReadSnapshot(reg.SHA, "agents/"+id+"/card.md"); err == nil && len(raw) <= 65536 {
			_, err = protocol.Unmarshal(raw, &c)
			ok = err == nil
		}
	}
	if ok {
		if name := strings.TrimSpace(c.DisplayName); name != "" {
			v.Name = truncate(name, 60)
		}
		if text := strings.TrimSpace(c.AvatarText); text != "" {
			v.Text = truncate(text, 2)
		}
		if avatarColor.MatchString(c.AvatarColor) {
			v.Color = c.AvatarColor
		}
		v.Image = c.AvatarImage
		v.AvatarID = cardID
		v.Bio = truncate(strings.TrimSpace(c.Bio), 500)
		if c.AvatarCrop != nil {
			v.Crop = &avatarCrop{X: c.AvatarCrop.X, Y: c.AvatarCrop.Y, Width: c.AvatarCrop.Width, Height: c.AvatarCrop.Height}
		}
	}
	return v
}
func (w *Web) identity(id string) agentIdentity {
	reg, err := agents.Load(w.Store)
	if err != nil {
		return defaultIdentity(id)
	}
	role := ""
	for _, r := range reg.Roles {
		if r.ID == id {
			role = id
			break
		}
	}
	return w.identityAt(reg, id, role)
}
func (w *Web) identitiesAt(reg *agents.Registry, cs []conversation) map[string]agentIdentity {
	result := map[string]agentIdentity{}
	if reg != nil {
		for _, a := range reg.Agents {
			result[a.ID] = w.identityAt(reg, a.ID, "")
		}
	}
	for _, c := range cs {
		for _, id := range c.Participants {
			if _, ok := result[id]; !ok {
				result[id] = w.identityAt(reg, id, "")
			}
		}
	}
	return result
}
func (w *Web) identities(cs []conversation) map[string]agentIdentity {
	reg, _ := agents.Load(w.Store)
	return w.identitiesAt(reg, cs)
}

func identityLabel(v agentIdentity) string {
	s := v.Name
	if v.ID != "" && v.ID != v.Name {
		s += " · " + v.ID
	}
	return s
}
func identityText(v agentIdentity) string {
	return `<span class="identity-text"><span class="identity-name">` + esc(v.Name) + `</span><span class="identity-id">` + esc(v.ID) + `</span></span>`
}
func identityBadge(v agentIdentity) string {
	return `<span class="identity-badge">` + avatar(v) + identityText(v) + `</span>`
}
func avatar(v agentIdentity) string {
	color := v.Color
	if !avatarColor.MatchString(color) {
		color = "#466D59"
	}
	rgb, _ := strconv.ParseUint(color[1:], 16, 32)
	ink := "#FFFFFF"
	if ((rgb>>16)*299+((rgb>>8)&255)*587+(rgb&255)*114)/1000 > 150 {
		ink = "#17221B"
	}
	content := esc(v.Text)
	if v.Image != "" && protocol.ValidName(v.ID) {
		style := "width:100%;height:100%;object-fit:contain;left:0;top:0;"
		// The browser uses intrinsic image dimensions and a bounded SVG viewBox;
		// the source pixels are returned unchanged by the image endpoint.
		avatarID := v.AvatarID
		if avatarID == "" {
			avatarID = v.ID
		}
		src := "/d/avatar/" + url.PathEscape(avatarID)
		content += fmt.Sprintf(`<img src="%s" alt="" style="%s" onerror="this.remove()">`, esc(src), style)
		if c := v.Crop; c != nil && validCrop(*c) {
			content = esc(v.Text) + fmt.Sprintf(`<svg viewBox="%d %d %d %d" preserveAspectRatio="xMidYMid meet" aria-hidden="true"><image href="%s" onerror="this.parentNode.remove()"/></svg>`, c.X, c.Y, c.Width, c.Height, esc(src))
		}
	}
	return fmt.Sprintf(`<span class="avatar" role="img" aria-label="%s" style="background:%s;color:%s">%s</span>`, esc(identityLabel(v)), color, ink, content)
}
func validCrop(c avatarCrop) bool {
	return c.X >= 0 && c.X <= 16384 && c.Y >= 0 && c.Y <= 16384 && c.Width > 0 && c.Width <= 16384 && c.Height > 0 && c.Height <= 16384 && c.X+c.Width <= 16384 && c.Y+c.Height <= 16384
}

const maxAvatar = 2 << 20

// Pin each real directory before descending. A symlink at the allowed root
// itself must not promote some other repository directory into the allowlist.
func openAvatarDirectory(repo *os.Root, rel string) (*os.Root, error) {
	current := repo
	closeCurrent := func() {
		if current != repo {
			_ = current.Close()
		}
	}
	for _, part := range strings.Split(rel, "/") {
		expected, err := current.Lstat(part)
		if err != nil || !expected.IsDir() || expected.Mode()&os.ModeSymlink != 0 {
			closeCurrent()
			return nil, fmt.Errorf("invalid avatar directory")
		}
		next, err := current.OpenRoot(part)
		if err != nil {
			closeCurrent()
			return nil, err
		}
		actual, err := next.Stat(".")
		if err != nil || !os.SameFile(expected, actual) {
			_ = next.Close()
			closeCurrent()
			return nil, fmt.Errorf("avatar directory changed")
		}
		closeCurrent()
		current = next
	}
	return current, nil
}

func (w *Web) readAvatar(v agentIdentity) ([]byte, string, error) {
	p := v.Image
	if p == "" || strings.Contains(p, "\\") || strings.Contains(p, ":") || path.IsAbs(p) || path.Clean(p) != p {
		return nil, "", fmt.Errorf("invalid image path")
	}
	base := "agents/_assets/avatars/"
	if !strings.HasPrefix(p, base) {
		base = "agents/" + v.ID + "/"
		if !strings.HasPrefix(p, base) {
			return nil, "", fmt.Errorf("invalid image directory")
		}
	}
	// Root the open at the allowed directory, not merely the repo: symlinks
	// cannot expose other agents' files or arbitrary repository contents.
	repo, err := os.OpenRoot(w.Store.Dir)
	if err != nil {
		return nil, "", err
	}
	defer repo.Close()
	root, err := openAvatarDirectory(repo, strings.TrimSuffix(base, "/"))
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	f, err := root.Open(strings.TrimPrefix(p, base))
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxAvatar {
		return nil, "", fmt.Errorf("invalid image file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxAvatar+1))
	if err != nil || len(raw) > maxAvatar {
		return nil, "", fmt.Errorf("invalid image size")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || (format != "png" && format != "webp") || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 16384 || cfg.Height > 16384 || cfg.Width*cfg.Height > 32_000_000 {
		return nil, "", fmt.Errorf("invalid image")
	}
	if v.Crop != nil && (!validCrop(*v.Crop) || v.Crop.X+v.Crop.Width > cfg.Width || v.Crop.Y+v.Crop.Height > cfg.Height) {
		return nil, "", fmt.Errorf("invalid crop")
	}
	// Decode fully: a forged header or truncated body must not be served.
	if _, _, err = image.Decode(bytes.NewReader(raw)); err != nil {
		return nil, "", fmt.Errorf("invalid image body")
	}
	return raw, "image/" + format, nil
}
func (w *Web) avatarImage(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !protocol.ValidName(id) {
		http.NotFound(rw, r)
		return
	}
	v := w.identity(id)
	raw, mime, err := w.readAvatar(v)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	tag := fmt.Sprintf(`"%x"`, sha256.Sum256(raw))
	rw.Header().Set("Content-Type", mime)
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.Header().Set("Cache-Control", "private, max-age=300")
	rw.Header().Set("ETag", tag)
	if r.Header.Get("If-None-Match") == tag {
		rw.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = rw.Write(raw)
}
func branchIdentity(identities map[string]agentIdentity, w *Web, id string) agentIdentity {
	if v, ok := identities[id]; ok {
		return v
	}
	return w.identity(id)
}

// messageIdentity 优先使用信件保存的角色；已撤下的角色保留原标识，不漂移到新角色。
func (w *Web) messageIdentity(reg *agents.Registry, m protocol.Message) agentIdentity {
	if m.From == "owner" {
		return defaultIdentity("owner")
	}
	if m.FromRole != "" {
		if reg != nil {
			for _, r := range reg.Roles {
				if r.ID == m.FromRole {
					return w.identityAt(reg, m.From, m.FromRole)
				}
			}
		}
		v := defaultIdentity(m.From)
		v.Name = m.FromRole
		v.RoleID = m.FromRole
		return v
	}
	return w.identityAt(reg, m.From, "")
}

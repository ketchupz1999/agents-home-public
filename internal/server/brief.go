package server

import (
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/ketchupz1999/agents-home-public/internal/model"
	"github.com/ketchupz1999/agents-home-public/internal/store"
)

const nowPath = "NOW.md"

// refreshNow rewrites NOW.md on main when the derived overview changed.
// Called after anything that can touch goals/lines/items.
func (s *Server) refreshNow(actor string) error {
	g, err := model.Load(s.Store)
	if err != nil {
		return err
	}
	now := []byte(g.RenderNow())
	if old, _ := s.Store.Read(nowPath); string(old) == string(now) {
		return nil
	}
	return s.Store.CommitFile(nowPath, now, actor, "now: refresh")
}

func (s *Server) saveCard(name string, content []byte) error {
	rel := "agents/" + name + "/card.md"
	return s.Store.CommitFile(rel, content, name, "card: "+name)
}

func (s *Server) getNow(w http.ResponseWriter, r *http.Request, k *Key) {
	g, err := model.Load(authorizedSource{s.Store, k})
	if err != nil {
		internal(w, err)
		return
	}
	var focus strings.Builder
	if k.CanRead("profile/") {
		current, err := s.DB.GetFocus()
		if err != nil {
			internal(w, err)
			return
		}
		focus.WriteString("\n## 当前关注\n\n")
		visible := 0
		for _, id := range current.Items {
			n := g.Get("item:" + id)
			if n == nil || !k.CanRead(n.Path) {
				continue
			}
			visible++
			fmt.Fprintf(&focus, "- %s — `%s`\n", n.Title, n.Path)
		}
		if current.Revision == 0 {
			focus.WriteString("尚未设置。\n")
		} else if len(current.Items) == 0 {
			focus.WriteString("当前未置顶事项。\n")
		} else if visible == 0 {
			focus.WriteString("暂无可读的关注事项。\n")
		}
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Write([]byte(g.RenderNow() + focus.String()))
}

type briefRef struct {
	Path   string `json:"path"`
	Title  string `json:"title,omitempty"`
	Type   string `json:"type,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Status string `json:"status,omitempty"`
	Note   string `json:"note,omitempty"`
}

func ref(n *model.Node) briefRef {
	return briefRef{Path: n.Path, Title: n.Title, Type: n.Type, Kind: n.Kind, Status: n.Status}
}

// getBrief answers "what should I read to pick up <id>?" as an ordered list.
// It is computed per request so it can never drift from the files.
func (s *Server) getBrief(w http.ResponseWriter, r *http.Request, k *Key) {
	g, err := model.Load(authorizedSource{s.Store, k})
	if err != nil {
		internal(w, err)
		return
	}
	n := g.Get(r.PathValue("id"))
	if n == nil || !k.CanRead(n.Path) {
		fail(w, http.StatusNotFound, "not found; try `aspace now` for ids")
		return
	}
	visible := func(ns []*model.Node) []briefRef {
		out := []briefRef{}
		for _, x := range ns {
			if k.CanRead(x.Path) {
				out = append(out, ref(x))
			}
		}
		return out
	}

	ancestors, missing := g.Ancestors(n)
	gates := []briefRef{}
	for _, a := range append([]*model.Node{n}, ancestors...) {
		for _, id := range a.GatedBy {
			if gn := g.Get("goal:" + id); gn != nil && k.CanRead(gn.Path) {
				r := ref(gn)
				r.Note = "gates " + a.ID
				gates = append(gates, r)
			}
		}
	}

	// People touched by this node or anything above it.
	pids := map[string]bool{}
	for _, a := range append([]*model.Node{n}, ancestors...) {
		for _, p := range a.People {
			pids[p] = true
		}
	}
	people := []briefRef{}
	for id := range pids {
		if p := g.Get("person:" + id); p != nil && k.CanRead(p.Path) {
			people = append(people, ref(p))
		}
	}
	sort.Slice(people, func(i, j int) bool { return people[i].Path < people[j].Path })

	profile := []briefRef{}
	if files, err := s.Store.List("profile"); err == nil {
		// preferences first: it is what every agent needs before talking.
		sort.SliceStable(files, func(i, j int) bool {
			return strings.Contains(files[i], "preferences") && !strings.Contains(files[j], "preferences")
		})
		for _, f := range files {
			if k.CanRead(f) && path.Base(f) != "README.md" {
				profile = append(profile, briefRef{Path: f})
			}
		}
	}

	reads := []briefRef{}
	base := path.Dir(n.Path)
	for _, p := range n.Reads {
		rel, err := store.Clean(path.Join(base, p))
		if err != nil || !k.CanRead(rel) {
			continue
		}
		r := briefRef{Path: rel}
		if !s.Store.Exists(rel) {
			r.Note = "missing"
		}
		reads = append(reads, r)
	}

	recent := []map[string]any{}
	if n.Type == model.TypeItem && k.CanRead("items/"+n.ID) {
		msgs, _ := s.DB.ListGroupMessagesByItem(n.ID, 0)
		for i := len(msgs) - 1; i >= 0 && len(recent) < 5; i-- {
			m := msgs[i]
			if !s.canSeeGroup(m.GroupID, k) {
				continue
			}
			recent = append(recent, map[string]any{"id": m.ID, "from": m.From, "to": m.To, "kind": m.Kind, "task": m.Task})
		}
	}

	ok(w, map[string]any{
		"target":   ref(n),
		"chain":    visible(ancestors),
		"gated_by": gates,
		"children": visible(g.Children(n)),
		"people":   people,
		"profile":  profile,
		"reads":    reads,
		"recent":   recent,
		"missing":  missing,
	})
}

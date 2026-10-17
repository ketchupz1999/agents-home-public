// Package model reads the content model out of the data repo: goals, lines,
// items and people, linked by front matter fields. Nothing here is stored;
// reverse links, NOW.md and briefs are all derived on demand.
package model

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	TypeGoal   = "goal"
	TypeLine   = "line"
	TypeItem   = "item"
	TypePerson = "person"
)

type Relation struct {
	To   string `yaml:"to" json:"to"`
	Type string `yaml:"type" json:"type"`
}

// Node is one entity. Only fields relevant to its type are set.
type Node struct {
	ID    string `yaml:"-" json:"id"`
	Type  string `yaml:"type" json:"type"`
	Path  string `yaml:"-" json:"path"`
	Title string `yaml:"-" json:"title"`

	Kind    string   `yaml:"kind,omitempty" json:"kind,omitempty"` // goal: outcome|explore|maintain|gated; person: person|group
	Status  string   `yaml:"status,omitempty" json:"status,omitempty"`
	Role    string   `yaml:"role,omitempty" json:"role,omitempty"` // line: main|side
	Serves  []string `yaml:"serves,omitempty" json:"serves,omitempty"`
	GatedBy []string `yaml:"gated_by,omitempty" json:"gated_by,omitempty"`
	Domains []string `yaml:"domains,omitempty" json:"domains,omitempty"`
	People  []string `yaml:"people,omitempty" json:"people,omitempty"`
	Reads   []string `yaml:"reads,omitempty" json:"reads,omitempty"`

	DisplayName string     `yaml:"display_name,omitempty" json:"display_name,omitempty"`
	Tier        string     `yaml:"tier,omitempty" json:"tier,omitempty"`
	Relations   []Relation `yaml:"relations,omitempty" json:"relations,omitempty"`
	Members     []string   `yaml:"members,omitempty" json:"members,omitempty"`
}

// Source is the minimal read access the model needs.
type Source interface {
	Read(rel string) ([]byte, error)
	List(dir string) ([]string, error)
	Dirs(dir string) ([]string, error)
}

// Graph indexes every node by "type/id" and by bare id.
type Graph struct {
	Nodes []*Node
	byKey map[string]*Node
}

func key(typ, id string) string { return typ + "/" + id }

// Parse reads front matter if present; files without it still load, with the
// type implied by their location.
func Parse(rel string, data []byte, defType, id string) *Node {
	n := &Node{}
	body := strings.ReplaceAll(string(data), "\r\n", "\n")
	if strings.HasPrefix(body, "---\n") {
		if end := strings.Index(body[4:], "\n---\n"); end >= 0 {
			_ = yaml.Unmarshal([]byte(body[4:4+end+1]), n)
			body = body[4+end+5:]
		}
	}
	n.ID, n.Path = id, rel
	if n.Type == "" {
		n.Type = defType
	}
	if n.Status == "" && n.Type != TypePerson {
		n.Status = "active"
	}
	n.Title = n.DisplayName
	for _, line := range strings.Split(body, "\n") {
		if t, ok := strings.CutPrefix(strings.TrimSpace(line), "# "); ok && n.Title == "" {
			// pj-add titles carry a " — 工作上下文" suffix that only adds noise.
			n.Title = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "— 工作上下文"))
			break
		}
	}
	if n.Title == "" {
		n.Title = id
	}
	return n
}

func Load(src Source) (*Graph, error) {
	g := &Graph{byKey: map[string]*Node{}}
	add := func(rel, typ, id string) error {
		b, err := src.Read(rel)
		if err != nil {
			return nil // missing CONTEXT.md etc. is not fatal
		}
		n := Parse(rel, b, typ, id)
		g.Nodes = append(g.Nodes, n)
		g.byKey[key(n.Type, id)] = n
		return nil
	}
	for dir, typ := range map[string]string{"goals": TypeGoal, "lines": TypeLine, "people": TypePerson} {
		files, err := src.List(dir)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if path.Dir(f) != dir || !strings.HasSuffix(f, ".md") || path.Base(f) == "README.md" {
				continue
			}
			add(f, typ, strings.TrimSuffix(path.Base(f), ".md"))
		}
	}
	items, err := src.Dirs("items")
	if err != nil {
		return nil, err
	}
	for _, id := range items {
		add("items/"+id+"/CONTEXT.md", TypeItem, id)
	}
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].Path < g.Nodes[j].Path })
	return g, nil
}

// Get resolves an id, preferring item > line > goal > person when ambiguous.
// "goal:x" style ids pick a type explicitly.
func (g *Graph) Get(id string) *Node {
	if typ, rest, ok := strings.Cut(id, ":"); ok {
		return g.byKey[key(typ, rest)]
	}
	for _, t := range []string{TypeItem, TypeLine, TypeGoal, TypePerson} {
		if n := g.byKey[key(t, id)]; n != nil {
			return n
		}
	}
	return nil
}

// parent resolves a serves/gated_by target, which can only be a goal or line.
func (g *Graph) parent(id string) *Node {
	if n := g.byKey[key(TypeLine, id)]; n != nil {
		return n
	}
	return g.byKey[key(TypeGoal, id)]
}

// Ancestors walks serves upward (and gated_by for goals), breadth-first,
// without repeats. Unknown ids are returned in missing.
func (g *Graph) Ancestors(n *Node) (out []*Node, missing []string) {
	seen := map[*Node]bool{n: true}
	queue := []*Node{n}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, id := range cur.Serves {
			p := g.parent(id)
			if p == nil {
				missing = append(missing, id)
				continue
			}
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
				queue = append(queue, p)
			}
		}
	}
	return out, missing
}

// Children returns nodes whose serves includes n.
func (g *Graph) Children(n *Node) []*Node {
	var out []*Node
	for _, c := range g.Nodes {
		for _, id := range c.Serves {
			if p := g.parent(id); p == n {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

func label(n *Node) string {
	var tags []string
	if n.Role != "" {
		tags = append(tags, n.Role)
	}
	if n.Kind != "" {
		tags = append(tags, n.Kind)
	}
	if n.Status != "" && n.Status != "active" {
		tags = append(tags, n.Status)
	}
	s := fmt.Sprintf("%s `%s`", n.Title, n.ID)
	if len(tags) > 0 {
		s += " (" + strings.Join(tags, ", ") + ")"
	}
	return s
}

var statusOrder = map[string]int{"active": 0, "paused": 1, "done": 2, "dropped": 3}

func byStatus(ns []*Node) {
	sort.SliceStable(ns, func(i, j int) bool { return statusOrder[ns[i].Status] < statusOrder[ns[j].Status] })
}

// RenderNow produces NOW.md. People are left out on purpose: the overview is
// readable by every key, people are not.
func (g *Graph) RenderNow() string {
	var b strings.Builder
	b.WriteString("# NOW\n\n> 由 aspace-server 根据 goals/ lines/ items/ 的文件头部自动生成，不要手改。\n\n")
	placed := map[*Node]bool{}
	var walk func(n *Node, depth int)
	walk = func(n *Node, depth int) {
		kids := g.Children(n)
		byStatus(kids)
		for _, c := range kids {
			fmt.Fprintf(&b, "%s- %s: %s\n", strings.Repeat("  ", depth), c.Type, label(c))
			if !placed[c] {
				placed[c] = true
				walk(c, depth+1)
			}
		}
	}
	var goals, lines, items []*Node
	for _, n := range g.Nodes {
		switch n.Type {
		case TypeGoal:
			goals = append(goals, n)
		case TypeLine:
			lines = append(lines, n)
		case TypeItem:
			items = append(items, n)
		}
	}
	byStatus(goals)
	b.WriteString("## goals\n\n")
	if len(goals) == 0 {
		b.WriteString("（暂无）\n")
	}
	for _, gl := range goals {
		fmt.Fprintf(&b, "- **%s**\n", label(gl))
		if len(gl.GatedBy) > 0 {
			fmt.Fprintf(&b, "  - 等待前提：%s\n", strings.Join(gl.GatedBy, ", "))
		}
		walk(gl, 1)
	}
	// Loose = no serves target resolves (none given, or all point nowhere).
	var loose []*Node
	for _, n := range append(lines, items...) {
		attached := false
		for _, id := range n.Serves {
			if g.parent(id) != nil {
				attached = true
				break
			}
		}
		if !attached {
			loose = append(loose, n)
		}
	}
	if len(loose) > 0 {
		b.WriteString("\n## 未挂靠\n\n")
		byStatus(loose)
		for _, n := range loose {
			fmt.Fprintf(&b, "- %s: %s\n", n.Type, label(n))
			placed[n] = true
			walk(n, 1)
		}
	}
	return b.String()
}

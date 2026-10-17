package web

import (
	"strings"
	"testing"
)

func TestMarkdownRequiredBlocksAndEscaping(t *testing.T) {
	body := "# 标题\n\n**粗体** 和 `a < b`\n\n- 第一项\n- 第二项\n\n1. 步骤一\n2. 步骤二\n\n```go\n<script>alert(1)</script>\n```\n\n| 名称 | 值 |\n| --- | :---: |\n| **状态** | `ready` |\n\n<img src=https://evil onerror=alert(1)>"
	got := renderMarkdown(body)
	for _, want := range []string{"<h2>标题</h2>", "<strong>粗体</strong>", "<code>a &lt; b</code>", "<ul><li>", "<ol><li>", "<pre><code>&lt;script&gt;", `<table>`, `<th scope="col">名称</th>`, `<td><strong>状态</strong></td>`, "&lt;img"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q: %s", want, got)
		}
	}
	for _, unsafe := range []string{"<script>", "<img ", "<iframe"} {
		if strings.Contains(got, unsafe) {
			t.Fatalf("unsafe markup %q", unsafe)
		}
	}
}
func TestMarkdownTablePipesAndIncompleteSyntax(t *testing.T) {
	got := renderMarkdown("| key | value |\n| --- | --- |\n| `a|b` | x\\|y |\n\n```\nunclosed **not bold**")
	if !strings.Contains(got, "<code>a|b</code>") || !strings.Contains(got, "<td>x|y</td>") || !strings.Contains(got, "<pre><code>unclosed **not bold**</code></pre>") {
		t.Fatal(got)
	}
	for _, body := range []string{"**", "`", "中文**未闭合", "|not a table|", "\n\n"} {
		_ = renderMarkdown(body)
	}
}

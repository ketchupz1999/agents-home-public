package web

import (
	"fmt"
	"regexp"
	"strings"
)

var orderedList = regexp.MustCompile(`^\d+[.)]\s+`)
var tableDivider = regexp.MustCompile(`^:?-{3,}:?$`)

// A deliberately small Markdown subset. Raw HTML and image syntax never become
// HTML elements or network requests; every text leaf is escaped exactly once.
func renderMarkdown(body string) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	var b strings.Builder
	for i := 0; i < len(lines); {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			i++
			continue
		}
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			fence := line[:3]
			i++
			var code []string
			for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), fence) {
				code = append(code, lines[i])
				i++
			}
			if i < len(lines) {
				i++
			}
			b.WriteString(`<pre><code>` + esc(strings.Join(code, "\n")) + `</code></pre>`)
			continue
		}
		if level, text := markdownHeading(line); level > 0 {
			fmt.Fprintf(&b, "<h%d>%s</h%d>", level, renderInline(text, 0), level)
			i++
			continue
		}
		if line == "---" || line == "***" {
			b.WriteString(`<hr>`)
			i++
			continue
		}
		if i+1 < len(lines) && isMarkdownTable(line, lines[i+1]) {
			header := tableCells(line)
			b.WriteString(`<div class="markdown-table" tabindex="0" role="region" aria-label="消息表格"><table><thead><tr>`)
			for _, cell := range header {
				b.WriteString(`<th scope="col">` + renderInline(cell, 0) + `</th>`)
			}
			b.WriteString(`</tr></thead><tbody>`)
			i += 2
			for i < len(lines) && strings.Contains(lines[i], "|") && strings.TrimSpace(lines[i]) != "" {
				cells := tableCells(lines[i])
				b.WriteString(`<tr>`)
				for j := range header {
					cell := ""
					if j < len(cells) {
						cell = cells[j]
					}
					b.WriteString(`<td>` + renderInline(cell, 0) + `</td>`)
				}
				b.WriteString(`</tr>`)
				i++
			}
			b.WriteString(`</tbody></table></div>`)
			continue
		}
		if kind, _ := markdownList(line); kind != "" {
			b.WriteString("<" + kind + ">")
			for i < len(lines) {
				next, text := markdownList(strings.TrimSpace(lines[i]))
				if next != kind {
					break
				}
				b.WriteString(`<li>` + renderInline(text, 0) + `</li>`)
				i++
			}
			b.WriteString("</" + kind + ">")
			continue
		}
		if strings.HasPrefix(line, "> ") {
			b.WriteString(`<blockquote>` + renderInline(strings.TrimPrefix(line, "> "), 0) + `</blockquote>`)
			i++
			continue
		}
		var paragraph []string
		for i < len(lines) {
			current := strings.TrimSpace(lines[i])
			if current == "" {
				break
			}
			if len(paragraph) > 0 {
				heading, _ := markdownHeading(current)
				kind, _ := markdownList(current)
				if heading > 0 || kind != "" || strings.HasPrefix(current, "```") || strings.HasPrefix(current, "~~~") || strings.HasPrefix(current, "> ") || current == "---" || current == "***" || (i+1 < len(lines) && isMarkdownTable(current, lines[i+1])) {
					break
				}
			}
			paragraph = append(paragraph, renderInline(current, 0))
			i++
		}
		b.WriteString(`<p>` + strings.Join(paragraph, "<br>") + `</p>`)
	}
	return b.String()
}

func markdownHeading(line string) (int, string) {
	for n := 1; n <= 6; n++ {
		prefix := strings.Repeat("#", n) + " "
		if strings.HasPrefix(line, prefix) {
			level := n + 1
			if level > 4 {
				level = 4
			}
			return level, strings.TrimPrefix(line, prefix)
		}
	}
	return 0, ""
}
func markdownList(line string) (string, string) {
	for _, prefix := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(line, prefix) {
			return "ul", strings.TrimPrefix(line, prefix)
		}
	}
	if prefix := orderedList.FindString(line); prefix != "" {
		return "ol", strings.TrimPrefix(line, prefix)
	}
	return "", ""
}
func tableCells(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	var cells []string
	var cell strings.Builder
	code, escaped := false, false
	for _, r := range line {
		if escaped {
			if r != '|' && r != '\\' && r != '`' {
				cell.WriteRune('\\')
			}
			cell.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == '`' {
			code = !code
		}
		if r == '|' && !code {
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
		} else {
			cell.WriteRune(r)
		}
	}
	if escaped {
		cell.WriteRune('\\')
	}
	return append(cells, strings.TrimSpace(cell.String()))
}
func isMarkdownTable(header, divider string) bool {
	if !strings.Contains(header, "|") {
		return false
	}
	cells := tableCells(divider)
	if len(cells) != len(tableCells(header)) {
		return false
	}
	for _, cell := range cells {
		if !tableDivider.MatchString(cell) {
			return false
		}
	}
	return len(cells) > 0
}
func renderInline(text string, depth int) string {
	if depth > 4 {
		return esc(text)
	}
	var b strings.Builder
	for len(text) > 0 {
		if text[0] == '`' {
			if end := strings.Index(text[1:], "`"); end >= 0 {
				b.WriteString(`<code>` + esc(text[1:end+1]) + `</code>`)
				text = text[end+2:]
				continue
			}
		}
		if strings.HasPrefix(text, "**") {
			if end := strings.Index(text[2:], "**"); end >= 0 {
				b.WriteString(`<strong>` + renderInline(text[2:end+2], depth+1) + `</strong>`)
				text = text[end+4:]
				continue
			}
		}
		// Copy a complete plain-text run, preserving UTF-8 and escaping raw tags.
		end := 1
		for end < len(text) && text[end] != '`' && !strings.HasPrefix(text[end:], "**") {
			end++
		}
		b.WriteString(esc(text[:end]))
		text = text[end:]
	}
	return b.String()
}

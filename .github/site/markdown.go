package main

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

var (
	reLink   = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	reStrong = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reEm     = regexp.MustCompile(`(^|[^*])\*([^*]+)\*`)
	reHead   = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	reOrder  = regexp.MustCompile(`^\d+\.\s+(.*)$`)
)

func renderMarkdown(src string) string {
	var out strings.Builder
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")

	var para []string
	var list []string
	listOrdered := false
	var quote []string

	flushPara := func() {
		if len(para) == 0 {
			return
		}
		fmt.Fprintf(&out, "<p>%s</p>\n", inline(strings.Join(para, " ")))
		para = nil
	}
	flushList := func() {
		if len(list) == 0 {
			return
		}
		tag := "ul"
		if listOrdered {
			tag = "ol"
		}
		fmt.Fprintf(&out, "<%s>\n", tag)
		for _, item := range list {
			fmt.Fprintf(&out, "<li>%s</li>\n", inline(item))
		}
		fmt.Fprintf(&out, "</%s>\n", tag)
		list = nil
	}
	flushQuote := func() {
		if len(quote) == 0 {
			return
		}
		fmt.Fprintf(&out, "<blockquote><p>%s</p></blockquote>\n", inline(strings.Join(quote, " ")))
		quote = nil
	}
	flushAll := func() {
		flushPara()
		flushList()
		flushQuote()
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") {
			flushAll()
			lang := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
				code = append(code, lines[i])
			}
			class := ""
			if lang != "" {
				class = fmt.Sprintf(` class="lang-%s"`, html.EscapeString(lang))
			}
			fmt.Fprintf(&out, "<pre><code%s>%s</code></pre>\n",
				class, html.EscapeString(strings.Join(code, "\n")))
			continue
		}

		if trimmed == "" {
			flushAll()
			continue
		}

		if trimmed == "---" || trimmed == "***" {
			flushAll()
			out.WriteString("<hr>\n")
			continue
		}

		if m := reHead.FindStringSubmatch(trimmed); m != nil {
			flushAll()
			level := len(m[1])
			fmt.Fprintf(&out, "<h%d>%s</h%d>\n", level, inline(m[2]), level)
			continue
		}

		if strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") {
			flushAll()
			var rows [][]string
			for ; i < len(lines); i++ {
				t := strings.TrimSpace(lines[i])
				if !strings.HasPrefix(t, "|") || !strings.HasSuffix(t, "|") {
					break
				}
				rows = append(rows, splitRow(t))
			}
			i--
			writeTable(&out, rows)
			continue
		}

		if strings.HasPrefix(trimmed, "> ") || trimmed == ">" {
			flushPara()
			flushList()
			quote = append(quote, strings.TrimPrefix(strings.TrimPrefix(trimmed, ">"), " "))
			continue
		}

		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			flushPara()
			flushQuote()
			if len(list) > 0 && listOrdered {
				flushList()
			}
			listOrdered = false
			list = append(list, trimmed[2:])
			continue
		}
		if m := reOrder.FindStringSubmatch(trimmed); m != nil {
			flushPara()
			flushQuote()
			if len(list) > 0 && !listOrdered {
				flushList()
			}
			listOrdered = true
			list = append(list, m[1])
			continue
		}

		if len(list) > 0 && strings.HasPrefix(line, "  ") {
			list[len(list)-1] += " " + trimmed
			continue
		}

		flushList()
		flushQuote()
		para = append(para, trimmed)
	}
	flushAll()

	return out.String()
}

func splitRow(row string) []string {
	inner := strings.TrimSuffix(strings.TrimPrefix(row, "|"), "|")
	cells := strings.Split(inner, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

func isDivider(cells []string) bool {
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		if c == "" || strings.Trim(c, ":- ") != "" {
			return false
		}
	}
	return true
}

func writeTable(out *strings.Builder, rows [][]string) {
	if len(rows) == 0 {
		return
	}
	out.WriteString("<div class=\"scroll\"><table>\n")
	body := rows
	if len(rows) > 1 && isDivider(rows[1]) {
		out.WriteString("<thead><tr>")
		for _, c := range rows[0] {
			fmt.Fprintf(out, "<th>%s</th>", inline(c))
		}
		out.WriteString("</tr></thead>\n")
		body = rows[2:]
	}
	if len(body) > 0 {
		out.WriteString("<tbody>\n")
		for _, row := range body {
			out.WriteString("<tr>")
			for _, c := range row {
				fmt.Fprintf(out, "<td>%s</td>", inline(c))
			}
			out.WriteString("</tr>\n")
		}
		out.WriteString("</tbody>\n")
	}
	out.WriteString("</table></div>\n")
}

func inline(s string) string {
	var out strings.Builder
	rest := s
	for {
		start := strings.Index(rest, "`")
		if start < 0 {
			out.WriteString(emphasise(html.EscapeString(rest)))
			break
		}
		end := strings.Index(rest[start+1:], "`")
		if end < 0 {
			out.WriteString(emphasise(html.EscapeString(rest)))
			break
		}
		out.WriteString(emphasise(html.EscapeString(rest[:start])))
		fmt.Fprintf(&out, "<code>%s</code>", html.EscapeString(rest[start+1:start+1+end]))
		rest = rest[start+end+2:]
	}
	return out.String()
}

func emphasise(escaped string) string {
	s := reLink.ReplaceAllString(escaped, `<a href="$2">$1</a>`)
	s = reStrong.ReplaceAllString(s, `<strong>$1</strong>`)
	s = reEm.ReplaceAllString(s, `$1<em>$2</em>`)
	return s
}

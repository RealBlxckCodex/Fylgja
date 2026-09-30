package builtin

import (
	"io"
	"strings"

	"golang.org/x/net/html"
)

// HTMLToText wandelt HTML in bereinigten Text (Skripte/Styles werden entfernt, Links als [text](url)).
func HTMLToText(r io.Reader) (title, text string) {
	z := html.NewTokenizer(r)
	var sb strings.Builder
	skip := 0
	inTitle := false
	var href string
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return strings.TrimSpace(title), collapse(sb.String())
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			tag := string(name)
			switch tag {
			case "script", "style", "noscript", "svg", "iframe", "template":
				if tt == html.StartTagToken {
					skip++
				}
			case "title":
				inTitle = true
			case "br":
				sb.WriteString("\n")
			case "p", "div", "section", "article", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6", "header", "footer", "table":
				sb.WriteString("\n")
				if strings.HasPrefix(tag, "h") && len(tag) == 2 {
					sb.WriteString(strings.Repeat("#", int(tag[1]-'0')) + " ")
				}
				if tag == "li" {
					sb.WriteString("- ")
				}
			case "a":
				href = ""
				for hasAttr {
					var k, v []byte
					k, v, hasAttr = z.TagAttr()
					if string(k) == "href" {
						href = string(v)
					}
				}
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "script", "style", "noscript", "svg", "iframe", "template":
				if skip > 0 {
					skip--
				}
			case "title":
				inTitle = false
			case "a":
				if href != "" && strings.HasPrefix(href, "http") {
					sb.WriteString(" (" + href + ")")
				}
				href = ""
			case "p", "div", "li", "tr", "h1", "h2", "h3", "table":
				sb.WriteString("\n")
			}
		case html.TextToken:
			if skip > 0 {
				continue
			}
			t := string(z.Text())
			if inTitle {
				title += t
				continue
			}
			sb.WriteString(t)
		}
	}
}

func collapse(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	blank := 0
	for _, l := range lines {
		l = strings.Join(strings.Fields(l), " ")
		if l == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

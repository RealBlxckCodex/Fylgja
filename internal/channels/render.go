package channels

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// EscapeMarkdownV2 maskiert Telegram-MarkdownV2-Sonderzeichen in normalem Text.
func EscapeMarkdownV2(s string) string {
	const special = "_*[]()~`>#+-=|{}.!\\"
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(special, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func escapeCodeV2(s string) string {
	return strings.NewReplacer("\\", "\\\\", "`", "\\`").Replace(s)
}

// RenderTelegram rendert eine RichMessage als MarkdownV2 (Tabellen → Monospace-Block).
// Normaler Text wird vollständig maskiert (Modelle liefern unzuverlässiges Markdown);
// Code-Blöcke aus dem Text bleiben erhalten.
func RenderTelegram(m RichMessage) string {
	var parts []string
	for _, b := range m.Blocks {
		switch b.Type {
		case "text":
			parts = append(parts, renderTextV2(b.Text))
		case "code":
			parts = append(parts, "```"+b.Lang+"\n"+escapeCodeV2(b.Text)+"\n```")
		case "table":
			parts = append(parts, "```\n"+escapeCodeV2(TableMonospace(b.Rows))+"\n```")
		case "quote":
			var q []string
			for _, l := range strings.Split(b.Text, "\n") {
				q = append(q, ">"+EscapeMarkdownV2(l))
			}
			parts = append(parts, strings.Join(q, "\n"))
		case "progress":
			parts = append(parts, EscapeMarkdownV2(ProgressBar(b.Percent)+" "+b.Text))
		case "image", "file":
			if b.URL != "" {
				parts = append(parts, EscapeMarkdownV2(b.Name+": "+b.URL))
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// renderTextV2 maskiert Text, lässt aber ```-Blöcke als Code stehen.
func renderTextV2(s string) string {
	segs := strings.Split(s, "```")
	var b strings.Builder
	for i, seg := range segs {
		if i%2 == 1 && i < len(segs)-1 {
			lang, body, _ := strings.Cut(seg, "\n")
			if strings.ContainsAny(lang, " \t") {
				lang, body = "", seg
			}
			b.WriteString("```" + lang + "\n" + escapeCodeV2(strings.TrimRight(body, "\n")) + "\n```")
			continue
		}
		if i%2 == 1 { // unbalanciert: als Text behandeln
			b.WriteString(EscapeMarkdownV2("```" + seg))
			continue
		}
		b.WriteString(EscapeMarkdownV2(seg))
	}
	return b.String()
}

// RenderDiscord rendert als Discord-Markdown (Tabellen → Code-Block).
func RenderDiscord(m RichMessage) string {
	var parts []string
	for _, b := range m.Blocks {
		switch b.Type {
		case "text":
			parts = append(parts, neutralizeMentions(b.Text))
		case "code":
			parts = append(parts, "```"+b.Lang+"\n"+strings.ReplaceAll(b.Text, "```", "`​``")+"\n```")
		case "table":
			parts = append(parts, "```\n"+TableMonospace(b.Rows)+"\n```")
		case "quote":
			parts = append(parts, "> "+strings.ReplaceAll(neutralizeMentions(b.Text), "\n", "\n> "))
		case "progress":
			parts = append(parts, ProgressBar(b.Percent)+" "+b.Text)
		case "image", "file":
			if b.URL != "" {
				parts = append(parts, b.Name+": "+b.URL)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// neutralizeMentions verhindert @everyone/@here-Pings durch Modellausgaben.
func neutralizeMentions(s string) string {
	return strings.NewReplacer("@everyone", "@​everyone", "@here", "@​here").Replace(s)
}

// RenderPlain für Kanäle ohne Markdown.
func RenderPlain(m RichMessage) string {
	var parts []string
	for _, b := range m.Blocks {
		switch b.Type {
		case "table":
			parts = append(parts, TableMonospace(b.Rows))
		case "progress":
			parts = append(parts, ProgressBar(b.Percent)+" "+b.Text)
		default:
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// TableMonospace formatiert eine Tabelle als ausgerichteten Text.
func TableMonospace(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	w := map[int]int{}
	for _, r := range rows {
		for i, c := range r {
			w[i] = max(w[i], utf8.RuneCountInString(c))
		}
	}
	var b strings.Builder
	for ri, r := range rows {
		for i, c := range r {
			if i > 0 {
				b.WriteString(" | ")
			}
			b.WriteString(c + strings.Repeat(" ", w[i]-utf8.RuneCountInString(c)))
		}
		b.WriteString("\n")
		if ri == 0 && len(rows) > 1 {
			for i := 0; i < len(r); i++ {
				if i > 0 {
					b.WriteString("-+-")
				}
				b.WriteString(strings.Repeat("-", w[i]))
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// ProgressBar liefert einen Textbalken.
func ProgressBar(p int) string {
	p = min(max(p, 0), 100)
	n := p / 10
	return "[" + strings.Repeat("█", n) + strings.Repeat("░", 10-n) + fmt.Sprintf("] %d%%", p)
}

// Split teilt Text an Absatz-/Zeilengrenzen in Teile ≤ max Zeichen (Runen) und hält
// Code-Blöcke balanciert (offene ``` werden geschlossen und im nächsten Teil wieder geöffnet).
func Split(s string, maxLen int) []string {
	if maxLen <= 0 || utf8.RuneCountInString(s) <= maxLen {
		return []string{s}
	}
	var out []string
	var cur strings.Builder
	curLen := 0
	inCode := false
	fence := ""
	flush := func() {
		t := cur.String()
		if inCode {
			t += "\n```"
		}
		if strings.TrimSpace(t) != "" {
			out = append(out, t)
		}
		cur.Reset()
		curLen = 0
		if inCode {
			cur.WriteString(fence + "\n")
			curLen = utf8.RuneCountInString(fence) + 1
		}
	}
	budget := maxLen - 4 // Platz für schließendes ```
	for _, line := range strings.SplitAfter(s, "\n") {
		ll := utf8.RuneCountInString(line)
		for ll > budget { // überlange Zeile hart schneiden
			if curLen > 0 {
				flush()
			}
			rs := []rune(line)
			cur.WriteString(string(rs[:budget-curLen]))
			line = string(rs[budget-curLen:])
			ll = utf8.RuneCountInString(line)
			curLen = budget
			flush()
		}
		if curLen+ll > budget {
			flush()
		}
		cur.WriteString(line)
		curLen += ll
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "```") {
			if inCode {
				inCode = false
			} else {
				inCode, fence = true, t
			}
		}
	}
	if curLen > 0 {
		t := cur.String()
		if strings.TrimSpace(t) != "" {
			out = append(out, t)
		}
	}
	return out
}

// ApprovalText rendert den Text einer Approval-Karte.
func ApprovalText(a ApprovalCard) RichMessage {
	head := fmt.Sprintf("🛡️ Freigabe nötig – %s", a.DotName)
	if a.Resolved != "" {
		head = fmt.Sprintf("🛡️ %s – %s", a.DotName, a.Resolved)
	}
	blocks := []Block{
		{Type: "text", Text: head},
		{Type: "text", Text: fmt.Sprintf("Aktion: %s\nWerkzeug: %s (%s)\nRisiko: %s", a.Summary, a.Tool, a.Class, a.Risk)},
	}
	if a.Reason != "" {
		blocks = append(blocks, Block{Type: "text", Text: "Grund der Nachfrage: " + a.Reason})
	}
	if a.Rationale != "" {
		blocks = append(blocks, Block{Type: "quote", Text: truncateRunes(a.Rationale, 500)})
	}
	if a.Preview != "" {
		blocks = append(blocks, Block{Type: "code", Text: truncateRunes(a.Preview, 1500)})
	}
	if a.Resolved == "" {
		if a.StepUp {
			blocks = append(blocks, Block{Type: "text", Text: "Diese Aktion braucht eine Bestätigung per Passkey in der Web-UI."})
		}
		blocks = append(blocks, Block{Type: "text", Text: "Läuft ab: " + a.ExpiresAt.Format("02.01. 15:04")})
	}
	return RichMessage{Blocks: blocks}
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

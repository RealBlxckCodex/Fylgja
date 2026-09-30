package channels

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEscapeV2(t *testing.T) {
	got := RenderTelegram(Text("Preis: 3.50€ (inkl.) - siehe *hier*!\n```go\nfmt.Println(\"a`b\")\n```\nEnde."))
	want := "Preis: 3\\.50€ \\(inkl\\.\\) \\- siehe \\*hier\\*\\!\n```go\nfmt.Println(\"a\\`b\")\n```\nEnde\\."
	if got != want {
		t.Fatalf("\n%q\n%q", got, want)
	}
}

func TestSplitKeepsCodeBalanced(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("Einleitung\n```python\n")
	for i := 0; i < 400; i++ {
		sb.WriteString("print('zeile')\n")
	}
	sb.WriteString("```\nSchluss äöü\n")
	parts := Split(sb.String(), 2000)
	if len(parts) < 3 {
		t.Fatalf("zu wenige teile: %d", len(parts))
	}
	for i, p := range parts {
		if utf8.RuneCountInString(p) > 2000 {
			t.Fatalf("teil %d zu lang: %d", i, utf8.RuneCountInString(p))
		}
		if strings.Count(p, "```")%2 != 0 {
			t.Fatalf("teil %d unbalanciert", i)
		}
	}
	if !strings.HasPrefix(parts[1], "```python") {
		t.Fatal("code-block nicht wieder geöffnet")
	}
	long := strings.Repeat("x", 9000)
	for _, p := range Split(long, 4096) {
		if utf8.RuneCountInString(p) > 4096 {
			t.Fatal("überlange zeile nicht geschnitten")
		}
	}
}

func TestTableAndDiscordMentions(t *testing.T) {
	tab := TableMonospace([][]string{{"Anbieter", "Preis"}, {"Hetzner", "4€"}})
	if !strings.Contains(tab, "Hetzner  | 4€") {
		t.Fatal(tab)
	}
	if strings.Contains(RenderDiscord(Text("hey @everyone")), "@everyone") {
		t.Fatal("mention nicht neutralisiert")
	}
}

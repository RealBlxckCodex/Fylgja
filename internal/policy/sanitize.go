package policy

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
)

var (
	mdImage = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)[^)]*\)`)
	mdLink  = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)[^)]*\)`)
	rawURL  = regexp.MustCompile(`https?://[^\s<>"')]+`)
)

// SanitizeOutbound entfernt Exfiltrationswege aus ausgehenden Nachrichten (14.5 Punkt 3):
// Bilder nur von erlaubten Hosts, im getainteten Run keine Links mit Query-Daten.
func SanitizeOutbound(text string, tainted bool, imageHosts []string) string {
	text = mdImage.ReplaceAllStringFunc(text, func(m string) string {
		sub := mdImage.FindStringSubmatch(m)
		u, err := url.Parse(sub[2])
		if err != nil || !slices.Contains(imageHosts, strings.ToLower(u.Hostname())) || (tainted && u.RawQuery != "") {
			return "[Bild entfernt: " + sub[1] + "]"
		}
		return m
	})
	if !tainted {
		return text
	}
	text = mdLink.ReplaceAllStringFunc(text, func(m string) string {
		sub := mdLink.FindStringSubmatch(m)
		u, err := url.Parse(sub[2])
		if err != nil || u.RawQuery != "" || u.Fragment != "" {
			return sub[1] + " (Link entfernt)"
		}
		return m
	})
	return rawURL.ReplaceAllStringFunc(text, func(m string) string {
		u, err := url.Parse(m)
		if err != nil || u.RawQuery != "" {
			return "[Link mit Parametern entfernt]"
		}
		return m
	})
}

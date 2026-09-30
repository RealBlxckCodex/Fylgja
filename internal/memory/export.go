package memory

import (
	"bufio"
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// Export schreibt das Memory als Markdown-Baum (11.4 Punkt 6):
// core.md, semantic.md, procedural.md, notes/YYYY-MM.md, episodes/YYYY-MM.md.
// Jede Zeile trägt die ID als HTML-Kommentar, damit Bearbeitungen zurückimportiert werden können.
func (s *Service) Export(ctx context.Context, dot uuid.UUID) (map[string]string, error) {
	all, err := s.list(ctx, `SELECT `+cols+` FROM memories WHERE dot_id=$1 AND valid_to IS NULL ORDER BY created_at`, dot)
	if err != nil {
		return nil, err
	}
	files := map[string]*strings.Builder{}
	get := func(p, title string) *strings.Builder {
		if b, ok := files[p]; ok {
			return b
		}
		b := &strings.Builder{}
		fmt.Fprintf(b, "# %s\n\n", title)
		files[p] = b
		return b
	}
	for _, m := range all {
		var b *strings.Builder
		switch m.Tier {
		case Core:
			b = get("core.md", "Kern")
		case Semantic:
			b = get("semantic.md", "Fakten")
		case Procedural:
			b = get("procedural.md", "Vorgehensweisen")
		case Note:
			b = get("notes/"+m.CreatedAt.Format("2006-01")+".md", "Notizen "+m.CreatedAt.Format("01/2006"))
		case Episodic:
			b = get("episodes/"+m.CreatedAt.Format("2006-01")+".md", "Episoden "+m.CreatedAt.Format("01/2006"))
		}
		flags := ""
		if m.Origin == FromUntrusted {
			flags = " ⚠untrusted"
		}
		fmt.Fprintf(b, "- %s <!-- id:%s%s -->\n", strings.ReplaceAll(m.Content, "\n", " "), m.ID, flags)
	}
	out := map[string]string{}
	for p, b := range files {
		out[p] = b.String()
	}
	return out, nil
}

var lineRe = regexp.MustCompile(`^- (.*?)\s*<!-- id:([0-9a-f-]{36})[^>]*-->\s*$`)

// ImportChange ist eine erkannte Änderung aus bearbeitetem Markdown.
type ImportChange struct {
	ID      uuid.UUID `json:"id"`
	Old     string    `json:"old"`
	New     string    `json:"new"`
	Deleted bool      `json:"deleted"`
}

// DiffImport vergleicht bearbeitetes Markdown mit dem Bestand (letzte Änderung gewinnt).
func (s *Service) DiffImport(ctx context.Context, dot uuid.UUID, files map[string]string) ([]ImportChange, error) {
	current, err := s.list(ctx, `SELECT `+cols+` FROM memories WHERE dot_id=$1 AND valid_to IS NULL`, dot)
	if err != nil {
		return nil, err
	}
	byID := map[uuid.UUID]*Memory{}
	for _, m := range current {
		byID[m.ID] = m
	}
	seen := map[uuid.UUID]bool{}
	var changes []ImportChange
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		sc := bufio.NewScanner(strings.NewReader(files[p]))
		for sc.Scan() {
			mm := lineRe.FindStringSubmatch(sc.Text())
			if mm == nil {
				continue
			}
			id, err := uuid.Parse(mm[2])
			if err != nil {
				continue
			}
			seen[id] = true
			if cur, ok := byID[id]; ok && strings.TrimSpace(mm[1]) != strings.ReplaceAll(cur.Content, "\n", " ") {
				changes = append(changes, ImportChange{ID: id, Old: cur.Content, New: strings.TrimSpace(mm[1])})
			}
		}
	}
	for _, m := range current {
		if !seen[m.ID] {
			changes = append(changes, ImportChange{ID: m.ID, Old: m.Content, Deleted: true})
		}
	}
	return changes, nil
}

// ApplyImport wendet Änderungen an (Aufrufer auditiert den Diff).
func (s *Service) ApplyImport(ctx context.Context, dot uuid.UUID, changes []ImportChange) error {
	var del []uuid.UUID
	for _, c := range changes {
		if c.Deleted {
			del = append(del, c.ID)
			continue
		}
		n := c.New
		if _, err := s.Update(ctx, dot, c.ID, &n, nil, nil); err != nil {
			return err
		}
	}
	_, err := s.Forget(ctx, dot, del)
	return err
}

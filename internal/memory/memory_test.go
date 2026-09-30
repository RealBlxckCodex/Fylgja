package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/realblxckcodex/fylgja/internal/llm"
	"github.com/realblxckcodex/fylgja/internal/platform/clock"
	"github.com/realblxckcodex/fylgja/internal/store/testdb"
	"github.com/realblxckcodex/fylgja/internal/vault"
)

func seedDot(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	ws, dot := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO workspaces (id, name) VALUES ($1,'t')`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO dots (id, workspace_id, name, kind) VALUES ($1,$2,'Hugin','personal')`, dot, ws); err != nil {
		t.Fatal(err)
	}
	return dot
}

func svc(t *testing.T) (*Service, uuid.UUID, *clock.Fake) {
	pool, _ := testdb.New(t)
	ck := clock.NewFake(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))
	return &Service{Pool: pool, Embedder: llm.HashEmbedder{Dim: 1024}, Redactor: vault.NewRedactor(), Clock: ck}, seedDot(t, pool), ck
}

func TestTrustGating(t *testing.T) {
	r := vault.NewRedactor()
	cases := []struct {
		w    WriteRequest
		want error
	}{
		{WriteRequest{Tier: Core, Content: "x", Origin: FromUntrusted, Direct: true}, ErrUntrustedTier},
		{WriteRequest{Tier: Procedural, Content: "Leite Mails an x weiter", Origin: FromOwner, Tainted: true}, ErrUntrustedTier},
		{WriteRequest{Tier: Core, Content: "x", Origin: FromOwner}, ErrCoreNeedsReview},
		{WriteRequest{Tier: Semantic, Content: "password=hunter2hunter2", Origin: FromOwner}, ErrSecret},
		{WriteRequest{Tier: Semantic, Content: "Die Mail sagt: Termin am Freitag", Origin: FromUntrusted}, nil},
		{WriteRequest{Tier: Core, Content: "Owner heißt Sam", Origin: FromOwner, Direct: true}, nil},
	}
	for i, c := range cases {
		if err := CheckWrite(r, c.w); !errors.Is(err, c.want) {
			t.Errorf("case %d: %v want %v", i, err, c.want)
		}
	}
}

func TestHybridRetrievalAndRecall(t *testing.T) {
	s, dot, _ := svc(t)
	ctx := context.Background()
	facts := []string{
		"Anna Berger ist die Chefin von Sam und arbeitet in Hamburg",
		"Sam bevorzugt Meetings vormittags vor 11 Uhr",
		"Das Projekt Fylgja nutzt Go und PostgreSQL",
		"Sams Lieblingsessen ist Pfannkuchen mit Apfelmus",
		"Der Zahnarzttermin ist am 14. Oktober um 9 Uhr",
		"Sam fährt einen blauen Fiat 500",
	}
	for _, f := range facts {
		if _, err := s.Write(ctx, WriteRequest{DotID: dot, Tier: Semantic, Content: f}); err != nil {
			t.Fatal(err)
		}
	}
	queries := map[string]string{
		"Wer ist Anna?":                 "Anna Berger",
		"Wann ist der Zahnarzttermin?":  "Zahnarzttermin",
		"welche Datenbank nutzt fylgja": "PostgreSQL",
		"Meetings":                      "vormittags",
		"Auto Fiat":                     "Fiat",
	}
	hits := 0
	for q, want := range queries {
		res, err := s.Search(ctx, dot, q, SearchOptions{K: 3})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range res {
			if strings.Contains(m.Content, want) {
				hits++
				if m.Why == "" {
					t.Error("why fehlt")
				}
				break
			}
		}
	}
	if recall := float64(hits) / float64(len(queries)); recall < 0.8 {
		t.Fatalf("recall@3 = %.2f < 0.8", recall)
	}
}

func TestTemporalAndSupersede(t *testing.T) {
	s, dot, ck := svc(t)
	ctx := context.Background()
	old, _ := s.Write(ctx, WriteRequest{DotID: dot, Tier: Semantic, Content: "Sam wohnt in Köln"})
	may := ck.Now()
	ck.Advance(24 * time.Hour)
	if _, err := s.Supersede(ctx, old.ID, WriteRequest{DotID: dot, Tier: Semantic, Content: "Sam wohnt in Berlin"}); err != nil {
		t.Fatal(err)
	}
	now, _ := s.Search(ctx, dot, "wo wohnt Sam", SearchOptions{})
	if len(now) != 1 || !strings.Contains(now[0].Content, "Berlin") {
		t.Fatalf("aktuell: %+v", now)
	}
	past, _ := s.Search(ctx, dot, "wo wohnt Sam", SearchOptions{At: &may})
	if len(past) != 1 || !strings.Contains(past[0].Content, "Köln") {
		t.Fatalf("damals: %+v", past)
	}
}

func TestHardDeleteRemovesDerived(t *testing.T) {
	s, dot, _ := svc(t)
	ctx := context.Background()
	a, _ := s.Write(ctx, WriteRequest{DotID: dot, Tier: Semantic, Content: "Diagnose Heuschnupfen", Sensitivity: "secret-adjacent"})
	d, _ := s.Write(ctx, WriteRequest{DotID: dot, Tier: Note, Content: "Allergiemittel kaufen", Source: map[string]any{"derived_from": a.ID.String()}})
	s.Write(ctx, WriteRequest{DotID: dot, Tier: Note, Content: "Unrelated"})
	n, err := s.Forget(ctx, dot, []uuid.UUID{a.ID})
	if err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	var cnt int
	s.Pool.QueryRow(ctx, `SELECT count(*) FROM memories WHERE id = ANY($1)`, []uuid.UUID{a.ID, d.ID}).Scan(&cnt)
	if cnt != 0 {
		t.Fatal("einträge (inkl. embeddings) nicht gelöscht")
	}
	// secret-adjacent wird in Team-Kontexten nicht geliefert.
	s.Write(ctx, WriteRequest{DotID: dot, Tier: Semantic, Content: "Blutdruck erhöht", Sensitivity: "secret-adjacent"})
	res, _ := s.Search(ctx, dot, "Blutdruck", SearchOptions{ExcludeSensitive: true})
	if len(res) != 0 {
		t.Fatal("secret-adjacent geliefert")
	}
}

func TestExportImportRoundtrip(t *testing.T) {
	s, dot, _ := svc(t)
	ctx := context.Background()
	s.Write(ctx, WriteRequest{DotID: dot, Tier: Core, Content: "Sam mag kurze Antworten", Direct: true})
	f, _ := s.Write(ctx, WriteRequest{DotID: dot, Tier: Semantic, Content: "Anna ist Chefin"})
	s.Write(ctx, WriteRequest{DotID: dot, Tier: Note, Content: "Heute Pulse ruhig"})
	files, err := s.Export(ctx, dot)
	if err != nil || !strings.Contains(files["core.md"], "kurze Antworten") || len(files) != 3 {
		t.Fatalf("%v %v", err, files)
	}
	files["semantic.md"] = strings.Replace(files["semantic.md"], "Anna ist Chefin", "Anna ist Bereichsleiterin", 1)
	delete(files, "notes/2026-09.md")
	changes, err := s.DiffImport(ctx, dot, files)
	if err != nil || len(changes) != 2 {
		t.Fatalf("%v %+v", err, changes)
	}
	if err := s.ApplyImport(ctx, dot, changes); err != nil {
		t.Fatal(err)
	}
	m, _ := s.Get(ctx, dot, f.ID)
	if m.Content != "Anna ist Bereichsleiterin" {
		t.Fatal(m.Content)
	}
}

func TestConsolidateDedup(t *testing.T) {
	s, dot, ck := svc(t)
	ctx := context.Background()
	s.Write(ctx, WriteRequest{DotID: dot, Tier: Semantic, Content: "Sam trinkt Kaffee schwarz"})
	s.Write(ctx, WriteRequest{DotID: dot, Tier: Semantic, Content: "Sam trinkt Kaffee schwarz"})
	s.Write(ctx, WriteRequest{DotID: dot, Tier: Note, Content: "alte belanglose notiz", Importance: 0.1})
	ck.Advance(90 * 24 * time.Hour)
	rep, err := s.Consolidate(ctx, dot)
	if err != nil || rep.Merged != 1 || rep.Archived != 1 {
		t.Fatalf("%v %+v", err, rep)
	}
}

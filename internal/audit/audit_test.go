package audit

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestChainAndTamper(t *testing.T) {
	m := &Memory{}
	ws := uuid.New()
	for i := 0; i < 5; i++ {
		if err := m.Log(context.Background(), Entry{WorkspaceID: ws, Actor: "system", Action: "test", Target: "x", Detail: map[string]any{"i": i, "f": 1.5}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := Verify(m.Entries); err != nil {
		t.Fatal(err)
	}
	m.Entries[2].Detail["i"] = 99
	var eb *ErrBroken
	if err := Verify(m.Entries); !errors.As(err, &eb) || eb.ID != 3 {
		t.Fatalf("manipulation nicht erkannt: %v", err)
	}
}

func TestPGChain(t *testing.T) {
	pool, _ := testdbNew(t)
	p := &PG{Pool: pool}
	ctx := context.Background()
	ws := uuid.New()
	for i := 0; i < 3; i++ {
		if err := p.Log(ctx, Entry{WorkspaceID: ws, Actor: "system", Action: "a", Detail: map[string]any{"n": i, "x": 1.25, "s": "ä<>&"}}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := p.VerifyAll(ctx)
	if err != nil || n != 3 {
		t.Fatalf("verify: %d %v", n, err)
	}
	// Update ist per Trigger verboten (append-only).
	if _, err := pool.Exec(ctx, `UPDATE audit_log SET actor='evil' WHERE id=2`); err == nil {
		t.Fatal("update erlaubt")
	}
	// Selbst bei deaktiviertem Trigger würde die Manipulation erkannt.
	pool.Exec(ctx, `ALTER TABLE audit_log DISABLE TRIGGER audit_log_append_only`)
	pool.Exec(ctx, `UPDATE audit_log SET detail='{"n":7}' WHERE id=2`)
	if _, err := p.VerifyAll(ctx); err == nil {
		t.Fatal("manipulation nicht erkannt")
	}
}

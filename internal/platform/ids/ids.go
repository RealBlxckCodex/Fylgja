// Package ids erzeugt zeitlich sortierbare UUIDv7-IDs (Spec 7).
package ids

import "github.com/google/uuid"

// New liefert eine neue UUIDv7.
func New() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		// NewV7 schlägt nur fehl, wenn crypto/rand nicht liest; das ist fatal.
		panic(err)
	}
	return id
}

// Generator ist injizierbar für deterministische Tests (Arbeitsregel 7).
type Generator interface{ New() uuid.UUID }

type v7 struct{}

func (v7) New() uuid.UUID { return New() }

// Default ist der produktive Generator.
var Default Generator = v7{}

// Seq ist ein deterministischer Generator für Tests.
type Seq struct{ n uint64 }

func (s *Seq) New() uuid.UUID {
	s.n++
	var u uuid.UUID
	for i := 0; i < 8; i++ {
		u[15-i] = byte(s.n >> (8 * i))
	}
	u[6] = 0x70 // Version 7
	u[8] = 0x80 // Variante
	return u
}

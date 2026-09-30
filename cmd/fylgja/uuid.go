package main

import (
	"fmt"
	"os"

	"github.com/google/uuid"
)

func mustUUID(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ungültige id:", s)
		os.Exit(1)
	}
	return id
}

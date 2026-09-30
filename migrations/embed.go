// Package migrations enthält die goose-SQL-Migrationen (Expand/Contract).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS

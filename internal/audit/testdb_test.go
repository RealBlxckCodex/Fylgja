package audit

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/realblxckcodex/fylgja/internal/store/testdb"
)

func testdbNew(t *testing.T) (*pgxpool.Pool, string) { return testdb.New(t) }

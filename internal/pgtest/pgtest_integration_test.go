//go:build integration

package pgtest_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Conte777/infra-mcp/internal/pgtest"
)

// Proves the harness itself works: a real postgres answers a real query.
func TestStartGivesAUsableDatabase(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.Start(t)

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(ctx); err != nil {
			t.Errorf("close: %v", err)
		}
	})

	var got int
	if err := conn.QueryRow(ctx, "select 1").Scan(&got); err != nil {
		t.Fatalf("select 1: %v", err)
	}

	if got != 1 {
		t.Fatalf("select 1 = %d, want 1", got)
	}
}

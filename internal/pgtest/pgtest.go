// Package pgtest starts throwaway postgres containers for integration tests.
package pgtest

import (
	"context"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Image is the postgres the integration suite runs against. It is the current
// release, not the oldest one supported (14, ADR-0004): the lower bound is a
// promise of the README that nothing here checks.
const Image = "postgres:17-alpine"

const (
	database = "infra_mcp_test"
	username = "infra_mcp"
	password = "infra_mcp"
)

// Start brings up a postgres container and returns its DSN. The container is
// torn down when the test ends, including on failure.
func Start(t *testing.T) string {
	t.Helper()

	ctx := context.Background()

	container, err := postgres.Run(ctx, Image,
		postgres.WithDatabase(database),
		postgres.WithUsername(username),
		postgres.WithPassword(password),
		// postgres restarts once during init, so the ready line appears twice;
		// BasicWaitStrategies accounts for that and adds a pg_isready probe
		postgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	return dsn
}

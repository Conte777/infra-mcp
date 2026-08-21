package postgres_test

import (
	"testing"

	"github.com/Conte777/infra-mcp/internal/mcpsrv/srvtest"
	"github.com/Conte777/infra-mcp/internal/source/postgres"
)

func TestConformance(t *testing.T) {
	srvtest.Conformance(t, postgres.Spec())
}

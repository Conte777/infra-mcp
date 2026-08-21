// Command infra-mcp-postgres serves the postgres source over MCP.
package main

import (
	"os"

	"github.com/Conte777/infra-mcp/internal/mcpsrv"
	"github.com/Conte777/infra-mcp/internal/source/postgres"
)

func main() {
	os.Exit(mcpsrv.Main(postgres.Spec()))
}

// Command infra-mcp-postgres serves the postgres source over MCP.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Conte777/infra-mcp/internal/buildinfo"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.Version())
		return
	}

	fmt.Fprintln(os.Stderr, "infra-mcp-postgres: server not implemented yet")
	os.Exit(1)
}

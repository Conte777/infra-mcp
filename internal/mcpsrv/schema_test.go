package mcpsrv

import "testing"

// A test binary is never a release build, so this is the case --init hits from a
// clone: the URL must name a ref that exists.
func TestSchemaURLFallsBackToMainOffRelease(t *testing.T) {
	got := SchemaURL("postgres")
	want := "https://raw.githubusercontent.com/Conte777/infra-mcp/main/schema/postgres.schema.json"
	if got != want {
		t.Fatalf("SchemaURL() = %q, want %q", got, want)
	}
}

package mcpsrv

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Conte777/infra-mcp/internal/mcpsrv/block"
)

func TestFailureTextIsHeadlineDetailThenHint(t *testing.T) {
	f := &Failure{
		Kind:   KindUnavailable,
		Detail: "dial db.example.com:5432: connection refused",
		Hint:   "check that the host is reachable from this machine",
	}

	want := "source unavailable: dial db.example.com:5432: connection refused\ncheck that the host is reachable from this machine"
	if f.Error() != want {
		t.Fatalf("Error() = %q, want %q", f.Error(), want)
	}
}

func TestEveryKindHasItsOwnHeadline(t *testing.T) {
	kinds := []Kind{KindInternal, KindUnavailable, KindTimeout, KindDenied, KindBadArgument, KindNotConfigured}
	seen := map[string]Kind{}
	for _, k := range kinds {
		if other, dup := seen[k.String()]; dup {
			t.Fatalf("kinds %d and %d share the headline %q", other, k, k.String())
		}
		seen[k.String()] = k
	}
}

func TestUnclassifiedErrorLosesItsTextButNotItsCause(t *testing.T) {
	raw := errors.New(`ERROR: relation "usrs" does not exist (SQLSTATE 42P01)`)

	f := asFailure(fmt.Errorf("listing columns: %w", raw))

	if f.Kind != KindInternal {
		t.Fatalf("Kind = %v, want KindInternal", f.Kind)
	}
	if strings.Contains(f.Error(), "42P01") {
		t.Fatalf("a raw source error reached the model: %q", f.Error())
	}
	if !errors.Is(f, raw) {
		t.Fatal("the cause was dropped, leaving nothing for the log")
	}
}

func TestFailureSurvivesWrapping(t *testing.T) {
	inner := &Failure{Kind: KindDenied, Detail: "role app may not read pg_authid"}

	f := asFailure(fmt.Errorf("querying pg_authid: %w", inner))

	if f != inner {
		t.Fatalf("asFailure() = %v, want the wrapped *Failure back", f)
	}
}

func TestExpiredDeadlineIsATimeoutNotAnInternalError(t *testing.T) {
	f := asFailure(fmt.Errorf("query: %w", context.DeadlineExceeded))

	if f.Kind != KindTimeout {
		t.Fatalf("Kind = %v, want KindTimeout", f.Kind)
	}
}

func TestFailureRendersThroughTheSameBlocks(t *testing.T) {
	f := &Failure{Kind: KindBadArgument, Detail: "database \"none\" is not in databases.exclude"}

	out := block.Markdown(f.blocks(), block.Budget{})

	if out != f.Error()+"\n" {
		t.Fatalf("Markdown() = %q, want the failure text", out)
	}
}

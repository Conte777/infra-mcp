package mcpsrv

import (
	"context"
	"errors"
	"strings"

	"github.com/Conte777/infra-mcp/internal/mcpsrv/block"
)

// Kind is the closed set of reasons a tool call fails. A source only maps its
// own error codes onto it; rendering stays with the core, which is what keeps
// a second representation from having a hole exactly on errors.
type Kind int

// The six kinds. Internal is the zero value on purpose: an error nobody
// classified is the one kind that tells the model nothing about the source.
const (
	KindInternal Kind = iota
	KindUnavailable
	KindTimeout
	KindDenied
	KindBadArgument
	KindNotConfigured
)

// String is the headline the model reads before the detail.
func (k Kind) String() string {
	switch k {
	case KindUnavailable:
		return "source unavailable"
	case KindTimeout:
		return "timed out"
	case KindDenied:
		return "permission denied"
	case KindBadArgument:
		return "invalid argument"
	case KindNotConfigured:
		return "not configured"
	default:
		return "internal error"
	}
}

// Failure is the only error shape that reaches the model.
type Failure struct {
	Kind Kind
	// Detail is the technical cause and nothing past it — host, port, error
	// class — never a guess at why ("the VPN may be down").
	Detail string
	// Hint is the next move, when there is one.
	Hint string
	// Err is the cause, kept for the log. It never reaches the model.
	Err error
}

// Error implements error.
func (f *Failure) Error() string {
	var b strings.Builder
	b.WriteString(f.Kind.String())
	if f.Detail != "" {
		b.WriteString(": ")
		b.WriteString(f.Detail)
	}
	if f.Hint != "" {
		b.WriteString("\n")
		b.WriteString(f.Hint)
	}
	return b.String()
}

// Unwrap implements the errors chain.
func (f *Failure) Unwrap() error { return f.Err }

func (f *Failure) blocks() []block.Block { return []block.Block{block.Text(f.Error())} }

const internalDetail = "the server failed while handling the call; the cause is in the server log"

// asFailure turns whatever a handler returned into the one shape the model
// sees. An unclassified error loses its text: a forgotten `return err` would
// otherwise hand the model a raw SQLSTATE, and closing that off here closes it
// off for all six sources at once.
func asFailure(err error) *Failure {
	var f *Failure
	if errors.As(err, &f) {
		return f
	}
	// A deadline reaching the core means the source-side limit never fired.
	// Calling that internal would hide the case the client-side deadline
	// exists to catch.
	if errors.Is(err, context.DeadlineExceeded) {
		return &Failure{
			Kind:   KindTimeout,
			Detail: "the call was still running when its deadline expired",
			Err:    err,
		}
	}
	return &Failure{Kind: KindInternal, Detail: internalDetail, Err: err}
}

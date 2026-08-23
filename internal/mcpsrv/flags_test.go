package mcpsrv

import (
	"log/slog"
	"testing"
)

func TestFlagDefaultsAreStdio(t *testing.T) {
	opts, err := parseFlags("postgres", nil)
	if err != nil {
		t.Fatalf("parseFlags() = %v", err)
	}

	if opts.httpAddr != "" {
		t.Errorf("httpAddr = %q, want stdio", opts.httpAddr)
	}
	if opts.logLevel != slog.LevelInfo {
		t.Errorf("logLevel = %v, want info", opts.logLevel)
	}
}

// The transport is a flag and never a config key: with no config the server
// still has to come up and say so.
func TestTransportComesFromTheFlag(t *testing.T) {
	opts, err := parseFlags("postgres", []string{"-http", "127.0.0.1:8080"})
	if err != nil {
		t.Fatalf("parseFlags() = %v", err)
	}

	if opts.httpAddr != "127.0.0.1:8080" {
		t.Errorf("httpAddr = %q, want the flag's value", opts.httpAddr)
	}
}

func TestBadFlagsAreRejected(t *testing.T) {
	for _, args := range [][]string{
		{"-log-level", "chatty"},
		{"-nonsense"},
		// The profile is gone, and a config that still passes it must not be
		// answered by reading some other environment's file.
		{"-profile", "stage"},
	} {
		t.Run(args[0], func(t *testing.T) {
			if _, err := parseFlags("postgres", args); err == nil {
				t.Fatalf("parseFlags(%v) accepted the arguments", args)
			}
		})
	}
}

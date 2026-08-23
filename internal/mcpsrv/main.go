package mcpsrv

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Conte777/infra-mcp/internal/buildinfo"
)

// Spec is one server binary: the source plus the config surface the core needs
// to find, load, generate and initialise a config for it.
type Spec[C any] struct {
	// Name is the source name; the config file prefix, the environment variable
	// and the binary suffix all derive from it.
	Name string
	// Source is the domain half of the server.
	Source Source[C]
	// Defaults is the config a file is applied on top of, and the config a
	// degraded start keeps: zero settings would mean no output budget at all
	// and no confirmation on writes.
	Defaults C
	// Minimal is what --init writes.
	Minimal C
	// Types carries the schema constraints the jsonschema tag cannot express.
	Types TypeSchemas
}

// Exit codes. Usage is kept apart from failure so a wrapper can tell a typo in
// a flag from a server that could not run.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// Main owns the process — flags, config, logging, transports, tool registry and
// shutdown — so that a server binary is this call and nothing else.
func Main[C any, P ConfigPtr[C]](spec Spec[C]) int {
	opts, err := parseFlags(spec.Name, os.Args[1:])
	switch {
	case errors.Is(err, flag.ErrHelp):
		return exitOK
	case err != nil:
		return exitUsage
	}

	// The only two modes with no transport, so stdout is free to print on.
	switch {
	case opts.version:
		fmt.Println(buildinfo.Version())
		return exitOK
	case opts.printSchema:
		if err := PrintSchema[C](os.Stdout, spec.Types); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFailure
		}
		return exitOK
	}

	loc := Location{Source: spec.Name, Flag: opts.configPath}

	if opts.initConfig {
		path, err := Init[C, P](loc, spec.Minimal, SchemaURL(spec.Name))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFailure
		}
		fmt.Println(path)
		return exitOK
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: opts.logLevel}))
	return serve[C, P](spec, loc, opts, log)
}

type options struct {
	configPath  string
	httpAddr    string
	logLevel    slog.Level
	initConfig  bool
	printSchema bool
	version     bool
}

func parseFlags(source string, args []string) (options, error) {
	opts := options{logLevel: slog.LevelInfo}
	level := "info"

	fs := flag.NewFlagSet("infra-mcp-"+source, flag.ContinueOnError)
	fs.StringVar(&opts.configPath, "config", "",
		"config file to read; wins over "+Location{Source: source}.EnvVar()+" and the XDG default")
	fs.StringVar(&opts.httpAddr, "http", "",
		"serve streamable HTTP on this address instead of stdio; unauthenticated, so bind it to a loopback address")
	fs.StringVar(&level, "log-level", level, "debug, info, warn or error")
	fs.BoolVar(&opts.initConfig, "init", false, "write a minimal config file and exit")
	fs.BoolVar(&opts.printSchema, "print-config-schema", false, "print the config JSON Schema and exit")
	fs.BoolVar(&opts.version, "version", false, "print the version and exit")

	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if err := opts.logLevel.UnmarshalText([]byte(level)); err != nil {
		fmt.Fprintf(os.Stderr, "invalid -log-level %q: want debug, info, warn or error\n", level)
		return opts, err
	}
	return opts, nil
}

// NewRuntime prepares the runtime for inv. Settings are read off the global
// level even on a degraded start, where that level is the source's defaults:
// they belong to the server, not to a cluster.
func NewRuntime[C any, P ConfigPtr[C]](inv Inventory[C], degraded error, proc Process, log *slog.Logger) Runtime[C] {
	global := inv.Global
	return Runtime[C]{
		Inventory: inv,
		Settings:  P(&global).Settings(),
		Degraded:  degraded,
		Process:   proc,
		Logger:    log,
	}
}

// Build assembles the server for spec: the source's own tools first, then the
// core's status tool.
func Build[C any](spec Spec[C], rt Runtime[C]) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "infra-mcp-" + spec.Name, Version: buildinfo.Version()},
		&mcp.ServerOptions{Instructions: instructions(spec, rt), Logger: rt.logger()},
	)
	r := NewRegistry(server, spec.Source.Prefix(), rt)
	spec.Source.Tools(r)
	registerStatus(r)
	return server
}

func serve[C any, P ConfigPtr[C]](spec Spec[C], loc Location, opts options, log *slog.Logger) int {
	transport := "stdio"
	if opts.httpAddr != "" {
		transport = "http " + opts.httpAddr
	}

	inv := Inventory[C]{Global: spec.Defaults}
	var degraded error
	if loaded, err := Load[C, P](loc, spec.Defaults, spec.Types); err != nil {
		degraded = err
	} else {
		inv = loaded
	}

	// Resolve again only to name the file in the log and in status; an error
	// here is the same one Load already turned into a degraded start.
	path, _, _ := loc.Resolve()

	proc := Process{Source: spec.Name, ConfigPath: path, Transport: transport}
	server := Build(spec, NewRuntime[C, P](inv, degraded, proc, log))

	log.Info("starting",
		"source", spec.Name, "config", path, "clusters", len(inv.Clusters),
		"transport", transport, "version", buildinfo.Version())
	if degraded != nil {
		log.Warn("degraded start: every tool call answers with this", "reason", degraded)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	if opts.httpAddr != "" {
		err = serveHTTP(ctx, server, opts.httpAddr, log)
	} else {
		err = serveStdio(ctx, server, log)
	}

	// Closing the source under a call that is still running would pull the
	// pool out from under it; the process is leaving anyway, so leave it be.
	if errors.Is(err, errDrainTimeout) {
		log.Warn("leaving with calls still running", "grace", shutdownGrace)
		return exitOK
	}
	if closeErr := spec.Source.Close(); closeErr != nil {
		log.Error("source did not close cleanly", "error", closeErr)
	}
	if err != nil && !isShutdown(err) {
		log.Error("server stopped", "error", err)
		return exitFailure
	}
	log.Info("stopped")
	return exitOK
}

// codeServerClosing is what the SDK reports when the session ends — over stdio,
// on every clean exit. The code is stable on the wire; the error value it comes
// from lives in an internal package, and it wraps the EOF as text, not as a cause.
const codeServerClosing = -32004

// isShutdown reports whether err is one of the ways a served session ends with
// nothing wrong: the client hung up, or we closed on a signal.
func isShutdown(err error) bool {
	var wire *jsonrpc.Error
	if errors.As(err, &wire) && wire.Code == codeServerClosing {
		return true
	}
	return errors.Is(err, io.EOF) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, http.ErrServerClosed)
}

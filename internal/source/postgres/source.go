package postgres

import (
	"github.com/Conte777/infra-mcp/internal/mcpsrv"
)

// Prefix starts every tool name of this source.
const Prefix = "pg"

// Source is the postgres half of the server: what the core does not own.
type Source struct{}

// Spec is the whole postgres server, ready for [mcpsrv.Main].
func Spec() mcpsrv.Spec[Config] {
	return mcpsrv.Spec[Config]{
		Name:     Name,
		Source:   &Source{},
		Defaults: Defaults(),
		Minimal:  Minimal(),
		Types:    ConfigTypes(),
	}
}

// Prefix implements [mcpsrv.Source].
func (*Source) Prefix() string { return Prefix }

// Instructions implements [mcpsrv.Source].
func (*Source) Instructions() string { return instructions }

// Tools implements [mcpsrv.Source]. The source's own tools land with the
// connection and the tool set; the core's status tool is registered either way.
func (*Source) Tools(*mcpsrv.Registry[Config]) {}

// Close implements [mcpsrv.Source].
func (*Source) Close() error { return nil }

// instructions go out once, at initialize — before any connection exists, so
// nothing here can be a live list of databases.
const instructions = `This server reaches one postgres server, configured ahead of time.

Tool names read <prefix>_<read|write>_<action>: a pg_read_ tool never changes
anything, a pg_write_ tool always asks first. Which database a tool talks to is
an argument of that tool, not a property of this server.

pg_read_status reports which config file is loaded; when the server is not
configured, every tool answers with the reason instead.`

package mcpsrv

import (
	"context"
	"strconv"

	"github.com/Conte777/infra-mcp/internal/buildinfo"
	"github.com/Conte777/infra-mcp/internal/mcpsrv/block"
)

// Env is what the core can say about itself: which config it settled on, and
// how it is being spoken to.
type Env struct {
	Source     string
	Profile    string
	ConfigPath string // empty when no config file was found
	Transport  string // "stdio", or "http <addr>"
}

const statusAction = "status"

// registerStatus adds <prefix>_read_status. The core registers it, not the
// source: what it reports is core knowledge, and all six servers must answer it
// the same way. On a degraded start the call never reaches this handler — [call]
// answers it with the config diagnosis, which is what "what is wrong" needs.
func registerStatus[C any](r *Registry[C]) {
	name := r.prefix + "_read_" + statusAction
	for _, t := range r.tools {
		if t.Name == name {
			panic("mcpsrv: " + name + " belongs to the core; the source must not register it")
		}
	}

	Read(r, statusAction, "report which config this server loaded and how it is running",
		func(context.Context, C, struct{}) ([]block.Block, error) {
			return []block.Block{status(r)}, nil
		})
}

func status[C any](r *Registry[C]) block.KeyValues {
	env, set := r.rt.Env, r.rt.Settings
	path := env.ConfigPath
	if path == "" {
		path = "(none found)"
	}
	return block.KeyValues{
		{Key: "source", Value: env.Source},
		{Key: "profile", Value: env.Profile},
		{Key: "config", Value: path},
		{Key: "transport", Value: env.Transport},
		{Key: "version", Value: buildinfo.Version()},
		{Key: "tools", Value: strconv.Itoa(len(r.tools))},
		{Key: "writeConfirmation", Value: strconv.FormatBool(set.Write.RequireConfirmation)},
		{Key: "maxRows", Value: strconv.Itoa(set.Output.MaxRows)},
		{Key: "maxBytes", Value: strconv.Itoa(set.Output.MaxBytes)},
		{Key: "maxCellChars", Value: strconv.Itoa(set.Output.MaxCellChars)},
	}
}

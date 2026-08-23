package mcpsrv

import (
	"context"
	"strconv"

	"github.com/Conte777/infra-mcp/internal/buildinfo"
	"github.com/Conte777/infra-mcp/internal/mcpsrv/block"
)

// Process is what the core can say about itself: which config it settled on,
// and how it is being spoken to. Not "environment": that word now names a
// group of clusters in the config.
type Process struct {
	Source     string
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

	// register, not [Read]: the core's own tool answers about the whole server,
	// and Read takes only arguments that name one cluster.
	register(r, accessRead, statusAction, "report which config this server loaded, which clusters it serves and how it is running",
		func(context.Context, C, struct{}) ([]block.Block, error) {
			blocks := []block.Block{status(r)}
			// A header with no rows under it is worse than no table: [Load]
			// leaves no server without a cluster, but [Build] can be called
			// with an inventory that was not assembled by it.
			if len(r.rt.Inventory.Clusters) > 0 {
				blocks = append(blocks, clusters(r.rt.Inventory))
			}
			return blocks, nil
		})
}

func status[C any](r *Registry[C]) block.KeyValues {
	proc, set := r.rt.Process, r.rt.Settings
	path := proc.ConfigPath
	if path == "" {
		path = "(none found)"
	}
	return block.KeyValues{
		{Key: "source", Value: proc.Source},
		{Key: "config", Value: path},
		{Key: "clusters", Value: strconv.Itoa(len(r.rt.Inventory.Clusters))},
		{Key: "transport", Value: proc.Transport},
		{Key: "version", Value: buildinfo.Version()},
		{Key: "tools", Value: strconv.Itoa(len(r.tools))},
		{Key: "writeConfirmation", Value: strconv.FormatBool(set.Write.RequireConfirmation)},
		{Key: "maxRows", Value: strconv.Itoa(set.Output.MaxRows)},
		{Key: "maxBytes", Value: strconv.Itoa(set.Output.MaxBytes)},
		{Key: "maxCellChars", Value: strconv.Itoa(set.Output.MaxCellChars)},
	}
}

// clusters names the addresses the count above only totals: past a handful of
// them this is the only place a human sees which ones exist and which refuse
// writes, and it needs no connection to answer. The host stays out — the
// answer lands in the context of a session that may never reach that cluster,
// and the config path above leads whoever needs it to the file.
func clusters[C any](inv Inventory[C]) block.Table {
	rows := make([][]any, 0, len(inv.Clusters))
	for _, c := range inv.Clusters {
		rows = append(rows, []any{c.Address.Environment, c.Address.Cluster, c.ReadOnly})
	}
	return block.Table{Columns: []string{"environment", "cluster", "readOnly"}, Rows: rows, Total: len(rows)}
}

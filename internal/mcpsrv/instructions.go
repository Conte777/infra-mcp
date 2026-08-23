package mcpsrv

import (
	"fmt"
	"strings"
)

// instructions is the source's text with the addresses this server serves
// under it. The core appends them rather than handing the config to the
// source: the names and readOnly are the core's, and with three required
// arguments — the two below plus the source's own — and no defaults, a model
// that does not know them cannot call a single tool.
func instructions[C any](spec Spec[C], rt Runtime[C]) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(spec.Source.Instructions(), "\n"))

	// The reason itself is left out: every tool call answers with it already.
	if rt.Degraded != nil {
		fmt.Fprintf(&b, "\n\nThis server reaches no clusters, because its config did not load.\n"+
			"Call %s_read_%s for the reason.\n", spec.Source.Prefix(), statusAction)
		return b.String()
	}

	// [Load] refuses a config that declares no cluster, so an empty inventory
	// here means [Build] was called with one assembled by other means.
	if len(rt.Inventory.Clusters) == 0 {
		return b.String() + "\n"
	}

	b.WriteString("\n\nClusters this server serves, written <environment>/<cluster> — the\n" +
		"environment and cluster arguments of every tool that names one;\n" +
		"(readOnly) means every write tool is refused there:\n")
	for _, c := range rt.Inventory.Clusters {
		b.WriteString("  " + c.Address.String())
		if c.ReadOnly {
			b.WriteString(" (readOnly)")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

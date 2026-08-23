package mcpsrv

import (
	"fmt"
	"maps"
)

// Address is the first two segments of an address into a source: which
// environment, and which cluster in it. The third segment — a database for
// postgres, a namespace for kubernetes — belongs to the source and never
// reaches the core.
type Address struct {
	Environment string `json:"environment" jsonschema:"environment holding the cluster, as named in the config"`
	Cluster     string `json:"cluster" jsonschema:"cluster inside that environment, as named in the config"`
}

// String prints the address the way the config nests it; a source appends its
// own segment to it.
func (a Address) String() string { return a.Environment + "/" + a.Cluster }

func (a Address) address() Address { return a }

// Addressed constrains a tool's arguments to ones that name a cluster.
// Embedding [Address] is the only way to satisfy it — the method is unexported
// — so the two arguments are the core's to declare and no tool can go out
// without them.
type Addressed interface{ address() Address }

// ClusterCommon is the part of a cluster config the core itself reads. A
// source's cluster type embeds it, the way its config type embeds [Common].
type ClusterCommon struct {
	ReadOnly bool `json:"readOnly,omitzero" jsonschema:"refuse every write tool at this address"`
}

func (c ClusterCommon) readOnly() bool { return c.ReadOnly }

// Environment is one environment of the config file. Besides its clusters it
// carries the settings they inherit, written straight into the same object;
// those keys are not declared here because Go cannot embed a type parameter —
// [Schema] adds them to the environment level instead.
type Environment[Cluster any] struct {
	Clusters map[string]Cluster `json:"clusters" jsonschema:"clusters of this environment, by name"`
}

// Cluster is one address of a loaded config together with the config in effect
// there, after inheritance.
type Cluster[C any] struct {
	Address  Address
	Config   C
	ReadOnly bool
}

// Inventory is what a config file amounts to: the global level, and every
// cluster it declares, ordered by environment and then by cluster name.
type Inventory[C any] struct {
	Global   C
	Clusters []Cluster[C]
}

// Find is the cluster at a, or the failure the tool call answers with.
func (inv Inventory[C]) Find(a Address) (Cluster[C], error) {
	for _, c := range inv.Clusters {
		if c.Address == a {
			return c, nil
		}
	}
	return Cluster[C]{}, &Failure{
		Kind:   KindBadArgument,
		Detail: fmt.Sprintf("this server serves no cluster %s", a),
		Hint:   "the environment and cluster arguments name one of the clusters this server was configured with",
	}
}

// The keys of a level object the core owns. Everything else in one is the
// source's cluster config, which the core moves between levels without reading.
const (
	keyEnvironments = "environments"
	keyClusters     = "clusters"
)

// inherit applies src on top of dst and returns the result, touching neither.
// Objects merge key by key; anything else — a scalar, a list — is replaced
// whole, so a list is never half-inherited.
func inherit(dst, src map[string]any) map[string]any {
	out := make(map[string]any, len(dst)+len(src))
	maps.Copy(out, dst)
	for k, v := range src {
		sub, isObject := v.(map[string]any)
		cur, wasObject := out[k].(map[string]any)
		if isObject && wasObject {
			out[k] = inherit(cur, sub)
			continue
		}
		out[k] = v
	}
	return out
}

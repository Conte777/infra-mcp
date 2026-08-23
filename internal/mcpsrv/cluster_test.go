package mcpsrv

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/Conte777/infra-mcp/internal/mcpsrv/block"
)

func TestInheritMergesObjectsAndReplacesTheRest(t *testing.T) {
	parse := func(s string) map[string]any {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	upper := parse(`{"connection":{"host":"h","user":"u"},"exclude":["a","b"],"port":1}`)
	lower := parse(`{"connection":{"host":"other"},"exclude":["c"]}`)

	got := inherit(upper, lower)

	// The list is replaced whole rather than appended to: merging two lists
	// would need a second rule on top of "the lower level wins".
	want := parse(`{"connection":{"host":"other","user":"u"},"exclude":["c"],"port":1}`)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("inherit = %v, want %v", got, want)
	}
	if len(upper) != 3 || len(upper["connection"].(map[string]any)) != 2 {
		t.Errorf("inherit wrote through to the level above it: %v", upper)
	}
}

// The three levels of the file are one shape: what a cluster may say, its
// environment may say for all of them, and the global level for the lot.
func TestSchemaCarriesTheClusterKeysAtEveryLevel(t *testing.T) {
	s, err := Schema[testConfig](nil)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}

	env := s.Properties[keyEnvironments].AdditionalProperties
	cluster := env.Properties[keyClusters].AdditionalProperties

	for name, level := range map[string]*jsonschema.Schema{"global": s, "environment": env, "cluster": cluster} {
		for _, key := range []string{"connection", "readOnly"} {
			if _, ok := level.Properties[key]; !ok {
				t.Errorf("the %s level takes no %q", name, key)
			}
		}
	}

	// Global keys stay global: an output budget per cluster would make the
	// shape of an answer depend on where it came from.
	for _, key := range []string{"output", "tools", keyEnvironments} {
		if _, ok := cluster.Properties[key]; ok {
			t.Errorf("the cluster level takes %q, which belongs to the server", key)
		}
	}
}

func TestWriteIsRefusedAtAReadOnlyCluster(t *testing.T) {
	addr := Address{Environment: "prod", Cluster: "main"}
	reached := false
	h := func(context.Context, testConfig, testIn) ([]block.Block, error) {
		reached = true
		return nil, nil
	}
	r := newTestRegistry(t, Runtime[testConfig]{Inventory: Inventory[testConfig]{
		Clusters: []Cluster[testConfig]{{Address: addr, ReadOnly: true}},
	}})

	_, err := call(t.Context(), r, accessWrite, h, testIn{Address: addr})

	if reached {
		t.Fatal("a write ran at a readOnly address")
	}
	if f := asFailure(err); f.Kind != KindDenied {
		t.Fatalf("Kind = %v, want KindDenied", f.Kind)
	}
	// The same address still reads: readOnly refuses the write, it does not
	// take the cluster out of service.
	if _, err := call(t.Context(), r, accessRead, h, testIn{Address: addr}); err != nil {
		t.Fatalf("read at a readOnly address = %v", err)
	}
}

func TestUnknownAddressIsABadArgument(t *testing.T) {
	r := newTestRegistry(t, Runtime[testConfig]{Inventory: testInventory()})

	_, err := call(t.Context(), r, accessRead, noop, testIn{Address: Address{Environment: "dev", Cluster: "nope"}})

	f := asFailure(err)
	if f.Kind != KindBadArgument {
		t.Fatalf("Kind = %v, want KindBadArgument", f.Kind)
	}
	if !strings.Contains(f.Detail, "dev/nope") {
		t.Errorf("Detail = %q, want the address that was asked for", f.Detail)
	}
}

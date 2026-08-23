package mcpsrv

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/Conte777/infra-mcp/internal/buildinfo"
)

// TypeSchemas maps a Go type to the schema it gets. It exists because the
// jsonschema tag carries a description and nothing else: an enum or a pattern
// has no other way in.
type TypeSchemas map[reflect.Type]*jsonschema.Schema

const metaSchema = "https://json-schema.org/draft/2020-12/schema"

// durationPattern is narrower than time.ParseDuration on purpose: "1.5h" and
// "500us" parse fine but read as mistakes in a config file.
const durationPattern = `^\d+(ms|s|m|h)$`

// Schema builds the JSON Schema for a source config: one truth, generated from
// the Go type, used both for editor completion and for validation at startup.
func Schema[C any](types TypeSchemas) (*jsonschema.Schema, error) {
	all := TypeSchemas{
		reflect.TypeFor[Duration](): {Type: "string", Pattern: durationPattern},
	}
	maps.Copy(all, types)

	s, err := jsonschema.For[C](&jsonschema.ForOptions{TypeSchemas: all})
	if err != nil {
		return nil, err
	}
	if err := spliceEnvironmentLevel(s); err != nil {
		return nil, err
	}
	s.Schema = metaSchema
	return s, nil
}

// spliceEnvironmentLevel gives the environment level the source's cluster keys.
// The generator cannot: [Environment] declares the clusters map and nothing of
// what a level carries, because Go has no way to embed a type parameter. The
// keys are copied off the cluster schema the generator did build, so the core
// still names no field of any source.
func spliceEnvironmentLevel(s *jsonschema.Schema) error {
	env, err := mapValue(s, keyEnvironments)
	if err != nil {
		return err
	}
	cluster, err := mapValue(env, keyClusters)
	if err != nil {
		return err
	}
	for name, prop := range cluster.Properties {
		if _, taken := env.Properties[name]; taken {
			return fmt.Errorf("the cluster config declares %q, which the core owns at the environment level", name)
		}
		// A copy, not the same pointer: a resolved schema has to be a tree.
		env.Properties[name] = prop.CloneSchemas()
	}
	env.PropertyOrder = append(env.PropertyOrder, cluster.PropertyOrder...)
	return nil
}

// mapValue is the schema of what property key holds, key being a map: the one
// place the three levels of the config are wired together.
func mapValue(s *jsonschema.Schema, key string) (*jsonschema.Schema, error) {
	prop := s.Properties[key]
	if prop == nil || prop.AdditionalProperties == nil {
		return nil, fmt.Errorf("%q is not a map of names in the generated schema; the config type does not embed mcpsrv.Common", key)
	}
	return prop.AdditionalProperties, nil
}

// PrintSchema writes the schema for a source config, for editors that will not
// fetch the published one.
func PrintSchema[C any](w io.Writer, types TypeSchemas) error {
	s, err := Schema[C](types)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

// SchemaURL is where the committed schema for source lives, pinned to the
// running binary's version so an older binary points at its own schema. Only a
// release is pinned: every other version names no tag, and the editor would be
// sent to a URL that does not resolve.
func SchemaURL(source string) string {
	ref := "main"
	if buildinfo.IsRelease() {
		ref = buildinfo.Version()
	}
	return "https://raw.githubusercontent.com/Conte777/infra-mcp/" + ref + "/schema/" + source + ".schema.json"
}

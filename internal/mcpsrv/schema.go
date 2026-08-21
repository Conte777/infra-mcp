package mcpsrv

import (
	"encoding/json"
	"io"
	"maps"
	"reflect"
	"strings"

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
	s.Schema = metaSchema
	return s, nil
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
// running binary's version so an older binary points at its own schema.
func SchemaURL(source string) string {
	ref := buildinfo.Version()
	if !strings.HasPrefix(ref, "v") {
		ref = "main"
	}
	return "https://raw.githubusercontent.com/Conte777/infra-mcp/" + ref + "/schema/" + source + ".schema.json"
}

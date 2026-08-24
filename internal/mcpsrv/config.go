// Package mcpsrv is the core every infra-mcp server is built from: it owns the
// process — flags, config, transports, tool registry, degraded start, rendering
// — and leaves a source with nothing but its own domain.
package mcpsrv

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Conte777/infra-mcp/internal/mcpsrv/block"
)

// Common is the part of a source config the core itself reads. Every source
// config embeds it, so the six stay identical where they must be. Alongside it
// a source config embeds its own cluster type: that is the global level, the
// settings every cluster below inherits.
type Common[Cluster any, ReadTools any] struct {
	Schema       string                          `json:"$schema,omitzero" jsonschema:"URL of the JSON Schema describing this file"`
	Output       Output                          `json:"output,omitzero" jsonschema:"limits on the size of a tool answer"`
	Tools        Tools[ReadTools]                `json:"tools,omitzero" jsonschema:"per-tool behaviour"`
	Environments map[string]Environment[Cluster] `json:"environments" jsonschema:"environments this server serves, by name"`
}

// Output caps a tool answer. maxBytes is measured on the rendered markdown —
// the only quantity that tracks tokens.
type Output struct {
	MaxRows      int `json:"maxRows,omitzero" jsonschema:"maximum rows in a table answer"`
	MaxBytes     int `json:"maxBytes,omitzero" jsonschema:"maximum bytes of rendered markdown"`
	MaxCellChars int `json:"maxCellChars,omitzero" jsonschema:"maximum characters in one table cell"`
}

// Budget is the output group as the renderer's cap on one answer.
func (o Output) Budget() block.Budget {
	return block.Budget{MaxRows: o.MaxRows, MaxBytes: o.MaxBytes, MaxCellChars: o.MaxCellChars}
}

// Tools splits into the write half the core owns and a read half each source
// shapes for itself.
type Tools[Read any] struct {
	Write WriteTools `json:"write,omitzero" jsonschema:"write tools"`
	Read  Read       `json:"read,omitzero" jsonschema:"read tools"`
}

// WriteTools is the core's half of the tools group: it decides whether write
// tools carry the confirmation marker.
type WriteTools struct {
	RequireConfirmation bool `json:"requireConfirmation,omitzero" jsonschema:"ask the user before every write tool call"`
}

// Settings is everything the core reads out of a source config.
type Settings struct {
	Output Output
	Write  WriteTools
}

// Settings implements [ConfigPtr].
func (c *Common[Cluster, ReadTools]) Settings() Settings {
	return Settings{Output: c.Output, Write: c.Tools.Write}
}

// Validate is the no-op a source overrides with the checks a schema cannot
// state. It runs on a cluster's config, once inheritance has filled it in.
func (c *Common[Cluster, ReadTools]) Validate() error { return nil }

func (c *Common[Cluster, ReadTools]) setSchemaURL(url string) { c.Schema = url }

// ConfigPtr constrains a source config to one that embeds [Common] and, for
// the cluster half of it, [ClusterCommon]. The unexported methods are the
// enforcement: no other type can satisfy it.
type ConfigPtr[C any] interface {
	*C
	Settings() Settings
	Validate() error
	setSchemaURL(string)
	readOnly() bool
}

// Duration is a config duration written as "30s": time.Duration marshals as a
// nanosecond count, unreadable in a hand-edited file.
type Duration time.Duration

// Duration returns the value as a [time.Duration].
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// MarshalJSON implements [json.Marshaler].
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON implements [json.Unmarshaler].
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// Location is where the config for one source may live.
type Location struct {
	Source string // "postgres"
	Flag   string // value of --config, empty when unset
}

// EnvVar is the per-source environment variable holding a config path. Per
// source, so that a stray export cannot point all six servers at one file.
func (l Location) EnvVar() string {
	return "INFRA_MCP_" + strings.ToUpper(l.Source) + "_CONFIG"
}

// XDGPath is the default location, $XDG_CONFIG_HOME/infra-mcp/<source>.json.
// One file per source and no more: every environment of every cluster lives in
// it.
func (l Location) XDGPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "infra-mcp", l.Source+".json")
}

// namedPath is the path the operator named — by --config, else by the
// environment variable — with the label an error shows it under; empty when
// neither is set. [Location.Resolve] and [Location.InitPath] both go through
// it, so --init cannot write where the server would not read.
func (l Location) namedPath() (path, label string) {
	if l.Flag != "" {
		return l.Flag, l.Flag
	}
	if p := os.Getenv(l.EnvVar()); p != "" {
		return p, l.EnvVar() + "=" + p
	}
	return "", ""
}

// InitPath is where --init writes, following the same order [Location.Resolve]
// reads in. Writing the XDG file while the environment variable points
// elsewhere would report success on a file the server never reads.
func (l Location) InitPath() string {
	if p, _ := l.namedPath(); p != "" {
		return p
	}
	return l.XDGPath()
}

// Resolve returns the config path and every location looked at, in the order
// --config > $INFRA_MCP_<SOURCE>_CONFIG > XDG. A path named explicitly is
// returned even when it does not exist: silently falling through to the XDG
// file would answer a question the operator did not ask — and would seat the
// server on another environment's database after a typo in the variable.
func (l Location) Resolve() (path string, searched []string, err error) {
	if p, label := l.namedPath(); p != "" {
		return p, []string{label}, nil
	}

	p := l.XDGPath()
	searched = append(searched, p)
	if fileExists(p) {
		return p, searched, nil
	}

	return "", searched, errors.New("no config file found")
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// ConfigError is any reason a config is unusable. It never aborts the process:
// the core turns it into a degraded start, where the full tool set is still
// registered and every call answers with this text.
type ConfigError struct {
	Searched []string // where the config was looked for
	Path     string   // the file settled on, empty when none was found
	Reason   string   // why it cannot be used
	Hint     string   // what to run
	Err      error    // underlying cause, if any
}

// Error renders the three parts in order: where we looked, what is wrong with
// what we found, what to do about it.
func (e *ConfigError) Error() string {
	var b strings.Builder
	if e.Path != "" {
		fmt.Fprintf(&b, "config %s: %s", e.Path, e.Reason)
	} else {
		fmt.Fprintf(&b, "no config: %s", e.Reason)
	}
	if len(e.Searched) > 0 {
		fmt.Fprintf(&b, "\nlooked in: %s", strings.Join(e.Searched, ", "))
	}
	if e.Hint != "" {
		fmt.Fprintf(&b, "\n%s", e.Hint)
	}
	return b.String()
}

// Unwrap implements the errors chain.
func (e *ConfigError) Unwrap() error { return e.Err }

const (
	initHint = "run with --init to create one"
	// --init refuses to overwrite, so it is no answer to a file that exists.
	schemaHint = "run with --print-config-schema for the keys this build accepts"
)

// noEnvironments is what a file with nothing to reach is told. It doubles as
// the migration note, because an 0.1 config is exactly this file with the
// source's keys still at the top level — but it does not claim the file is one.
const noEnvironments = "declares no environments: every cluster lives under " +
	"environments.<environment>.clusters.<cluster>, and the top level holds only what they inherit — " +
	"which is where the connection settings of an 0.1 config belong"

// Load reads the config for loc and resolves it into one config per cluster:
// the file's global level, then the environment, then the cluster, each level
// overriding the one above. Every failure is a *ConfigError, which the caller
// turns into a degraded start rather than an exit.
func Load[C any, P ConfigPtr[C]](loc Location, defaults C, types TypeSchemas) (Inventory[C], error) {
	var path string
	var searched []string
	var haveFile bool
	fail := func(reason string, err error) (Inventory[C], error) {
		hint := initHint
		if haveFile {
			hint = schemaHint
		}
		return Inventory[C]{}, &ConfigError{Searched: searched, Path: path, Reason: reason, Hint: hint, Err: err}
	}

	path, searched, err := loc.Resolve()
	if err != nil {
		return fail("no config file found", err)
	}
	// Existence, not readability: an unreadable file is still one --init refuses
	// to overwrite. Resolve returns a named path whether or not it is there.
	haveFile = fileExists(path)

	data, err := os.ReadFile(path) //nolint:gosec // the path is the operator's own --config, env or XDG choice
	if err != nil {
		return fail("cannot be read", err)
	}

	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fail("is not valid JSON: "+err.Error(), err)
	}
	root, ok := raw.(map[string]any)
	if !ok {
		return fail("is not a JSON object", nil)
	}
	// Before the schema, which would answer an 0.1 config with a heap of
	// unknown-key complaints and never say what replaced them. A present but
	// mistyped environments is left to the schema: it can name what it found
	// there, and this cannot.
	if envs, present := root[keyEnvironments]; !present || isEmptyObject(envs) {
		return fail(noEnvironments, nil)
	}

	// Validating the raw document, not the decoded struct: decoding drops an
	// unknown key silently, and a typo would take its setting with it.
	schema, err := Schema[C](types)
	if err != nil {
		return fail("schema cannot be built: "+err.Error(), err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return fail("schema cannot be resolved: "+err.Error(), err)
	}
	if err := resolved.Validate(raw); err != nil {
		return fail("does not match the schema: "+err.Error(), err)
	}

	// The global level is the whole document minus the environments under it.
	global := without(root, keyEnvironments)

	inv := Inventory[C]{}
	secrets := secretPaths[C]()
	inv.Global, err = decode(global, defaults, secrets)
	if err != nil {
		return fail(err.Error(), err)
	}

	// The schema has vouched for the shape of the document by now.
	environments, _ := root[keyEnvironments].(map[string]any)
	for _, envName := range slices.Sorted(maps.Keys(environments)) {
		env, _ := environments[envName].(map[string]any)
		clusters, _ := env[keyClusters].(map[string]any)
		if len(clusters) == 0 {
			return fail(fmt.Sprintf("environment %s declares no clusters", envName), nil)
		}
		level := inherit(global, without(env, keyClusters))
		// The environment level is checked on its own account: a literal secret
		// written here and overridden by every cluster under it would never
		// reach a merged level to be caught there.
		if err := checkSecrets(level, secrets); err != nil {
			return fail(envName+": "+err.Error(), err)
		}

		for _, name := range slices.Sorted(maps.Keys(clusters)) {
			cluster, _ := clusters[name].(map[string]any)
			addr := Address{Environment: envName, Cluster: name}
			cfg, err := decode(inherit(level, cluster), defaults, secrets)
			if err == nil {
				// Only a cluster is validated: the levels above it are free to
				// be incomplete, and a key missing from all three is reported
				// here, where it is finally missing.
				err = P(&cfg).Validate()
			}
			if err != nil {
				return fail(addr.String()+": "+err.Error(), err)
			}
			inv.Clusters = append(inv.Clusters, Cluster[C]{Address: addr, Config: cfg, ReadOnly: P(&cfg).readOnly()})
		}
	}
	return inv, nil
}

func isEmptyObject(v any) bool {
	m, ok := v.(map[string]any)
	return ok && len(m) == 0
}

// without is one level of the file with a key of the core's own taken out of
// it: what is left is the source's, and inherits as one object.
func without(level map[string]any, key string) map[string]any {
	out := maps.Clone(level)
	delete(out, key)
	return out
}

// decode turns one level object into a config: secrets are checked first,
// because after expansion a ${VAR} and a literal are the same string, and the
// round trip through JSON applies the object on top of defaults, so a key it
// leaves out keeps the default.
func decode[C any](level map[string]any, defaults C, secrets [][]segment) (C, error) {
	var zero C
	if err := checkSecrets(level, secrets); err != nil {
		return zero, err
	}
	expanded, err := expand(level)
	if err != nil {
		return zero, err
	}
	normalized, err := json.Marshal(expanded)
	if err != nil {
		return zero, fmt.Errorf("cannot be re-encoded: %w", err)
	}
	cfg := defaults
	if err := json.Unmarshal(normalized, &cfg); err != nil {
		return zero, fmt.Errorf("cannot be decoded: %w", err)
	}
	return cfg, nil
}

// Init writes the minimal config for loc and returns the path. Minimal, not
// complete-with-defaults: a file full of today's defaults would freeze them in
// place for every user who ran --init once.
func Init[C any, P ConfigPtr[C]](loc Location, minimal C, schemaURL string) (string, error) {
	path := loc.InitPath()
	if fileExists(path) {
		return path, fmt.Errorf("%s already exists", path)
	}

	P(&minimal).setSchemaURL(schemaURL)
	data, err := json.MarshalIndent(minimal, "", "  ")
	if err != nil {
		return path, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return path, err
	}
	// 0600: the file holds a connection, and will hold ${VAR} names pointing at secrets.
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return path, err
	}
	return path, nil
}

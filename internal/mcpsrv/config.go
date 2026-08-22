// Package mcpsrv is the core every infra-mcp server is built from: it owns the
// process — flags, config, transports, tool registry, degraded start, rendering
// — and leaves a source with nothing but its own domain.
package mcpsrv

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Conte777/infra-mcp/internal/mcpsrv/block"
)

// Common is the part of a source config the core itself reads. Every source
// config embeds it, so the six stay identical where they must be.
type Common[ReadTools any] struct {
	Schema string           `json:"$schema,omitzero" jsonschema:"URL of the JSON Schema describing this file"`
	Output Output           `json:"output,omitzero" jsonschema:"limits on the size of a tool answer"`
	Tools  Tools[ReadTools] `json:"tools,omitzero" jsonschema:"per-tool behaviour"`
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
func (c *Common[R]) Settings() Settings {
	return Settings{Output: c.Output, Write: c.Tools.Write}
}

// Validate is the no-op a source overrides with the checks a schema cannot state.
func (c *Common[R]) Validate() error { return nil }

func (c *Common[R]) setSchemaURL(url string) { c.Schema = url }

// ConfigPtr constrains a source config to one that embeds [Common]. The
// unexported method is the enforcement: no other type can satisfy it.
type ConfigPtr[C any] interface {
	*C
	Settings() Settings
	Validate() error
	setSchemaURL(string)
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

// DefaultProfile is the profile a server runs under when --profile is not given.
const DefaultProfile = "default"

// Location is where the config for one source and profile may live.
type Location struct {
	Source  string // "postgres"
	Profile string // "default"
	Flag    string // value of --config, empty when unset
}

// EnvVar is the per-source environment variable holding a config path. Per
// source, so that a stray export cannot point all six servers at one file.
func (l Location) EnvVar() string {
	return "INFRA_MCP_" + strings.ToUpper(l.Source) + "_CONFIG"
}

// XDGPath is the default location, $XDG_CONFIG_HOME/infra-mcp/<source>.<profile>.json.
func (l Location) XDGPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "infra-mcp", l.Source+"."+l.Profile+".json")
}

// InitPath is where --init writes, following the same order [Location.Resolve]
// reads in. Writing the XDG file while the environment variable points
// elsewhere would report success on a file the server never reads.
func (l Location) InitPath() string {
	if l.Flag != "" {
		return l.Flag
	}
	if p := os.Getenv(l.EnvVar()); p != "" {
		return p
	}
	return l.XDGPath()
}

// Resolve returns the config path and every location looked at, in the order
// --config > $INFRA_MCP_<SOURCE>_CONFIG > XDG. A path named explicitly is
// returned even when it does not exist: silently falling through to the XDG
// file would answer a question the operator did not ask.
func (l Location) Resolve() (path string, searched []string, err error) {
	if l.Flag != "" {
		return l.Flag, []string{l.Flag}, nil
	}

	if p := os.Getenv(l.EnvVar()); p != "" {
		searched = append(searched, l.EnvVar()+"="+p)
		if fileExists(p) {
			return p, searched, nil
		}
	}

	p := l.XDGPath()
	searched = append(searched, p)
	if fileExists(p) {
		return p, searched, nil
	}

	return "", searched, errors.New("no config file found")
}

func fileExists(p string) bool {
	st, err := os.Stat(p) //nolint:gosec // the path is the operator's own --config, env or XDG choice
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

const initHint = "run with --init to create one"

// Load reads the config for loc and applies it on top of defaults. Every
// failure is a *ConfigError, which the caller turns into a degraded start
// rather than an exit.
func Load[C any, P ConfigPtr[C]](loc Location, defaults C, types TypeSchemas) (C, error) {
	var path string
	var searched []string
	fail := func(reason string, err error) (C, error) {
		var zero C
		return zero, &ConfigError{Searched: searched, Path: path, Reason: reason, Hint: initHint, Err: err}
	}

	path, searched, err := loc.Resolve()
	if err != nil {
		return fail("no config file found", err)
	}

	data, err := os.ReadFile(path) //nolint:gosec // the path is the operator's own --config, env or XDG choice
	if err != nil {
		return fail("cannot be read", err)
	}

	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fail("is not valid JSON: "+err.Error(), err)
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

	// Before expansion: afterwards a ${VAR} and a literal look alike.
	if err := checkSecrets(raw, secretPaths[C]()); err != nil {
		return fail(err.Error(), err)
	}

	expanded, err := expand(raw)
	if err != nil {
		return fail(err.Error(), err)
	}

	// The round trip applies the file on top of defaults: a key absent from the
	// document leaves the default in place.
	normalized, err := json.Marshal(expanded)
	if err != nil {
		return fail("cannot be re-encoded: "+err.Error(), err)
	}
	cfg := defaults
	if err := json.Unmarshal(normalized, &cfg); err != nil {
		return fail("cannot be decoded: "+err.Error(), err)
	}

	if err := P(&cfg).Validate(); err != nil {
		return fail(err.Error(), err)
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

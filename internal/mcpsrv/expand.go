package mcpsrv

import (
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
)

// varRef matches ${VAR} and ${VAR:-default}, the same forms .mcp.json accepts.
var varRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// secretRef is a bare ${VAR}: a secret admits no default, which would let a
// literal in through the back door.
var secretRef = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)

// expand substitutes environment variables in every string of the decoded
// document. It runs once, at startup: the config is a value read on the way in,
// never re-read, so a rotated secret needs a restart.
func expand(v any) (any, error) {
	switch t := v.(type) {
	case string:
		return expandString(t)

	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			e, err := expand(val)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			out[k] = e
		}
		return out, nil

	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			e, err := expand(val)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out[i] = e
		}
		return out, nil

	default:
		return v, nil
	}
}

func expandString(s string) (string, error) {
	var missing []string

	out := varRef.ReplaceAllStringFunc(s, func(ref string) string {
		g := varRef.FindStringSubmatch(ref)
		if v, ok := os.LookupEnv(g[1]); ok {
			return v
		}
		// A name cannot contain ':', so ":-" in the match means a default was
		// given — possibly the empty one, which FindStringSubmatch cannot tell
		// apart from no default at all.
		if strings.Contains(ref, ":-") {
			return g[2]
		}
		missing = append(missing, g[1])
		return ref
	})

	if len(missing) > 0 {
		// Not an empty string: that surfaces later as an unexplained
		// authentication failure instead of a named missing variable.
		return "", fmt.Errorf("environment variable %s is not set", strings.Join(missing, ", "))
	}
	return out, nil
}

// secretPaths returns the JSON path of every field tagged `mcpsrv:"secret"`.
func secretPaths[C any]() [][]string {
	return secretFields(reflect.TypeFor[C](), nil)
}

func secretFields(t reflect.Type, prefix []string) [][]string {
	var out [][]string
	for _, f := range reflect.VisibleFields(t) {
		// Promoted fields come through separately, at the level they occupy in JSON.
		if f.Anonymous {
			continue
		}
		name := jsonName(f)
		if name == "" {
			continue
		}
		path := append(append([]string{}, prefix...), name)

		if f.Tag.Get("mcpsrv") == "secret" {
			out = append(out, path)
			continue
		}
		if f.Type.Kind() == reflect.Struct {
			out = append(out, secretFields(f.Type, path)...)
		}
	}
	return out
}

func jsonName(f reflect.StructField) string {
	if !f.IsExported() {
		return ""
	}
	tag := f.Tag.Get("json")
	if tag == "-" {
		return ""
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		name = f.Name
	}
	return name
}

// checkSecrets rejects a secret written as anything but a bare ${VAR}. It runs
// on the raw document: after expansion the literal and the reference are the
// same string.
func checkSecrets(raw any, paths [][]string) error {
	for _, p := range paths {
		v, ok := lookup(raw, p)
		if !ok {
			continue // absent or mistyped — the schema has already spoken
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		if !secretRef.MatchString(s) {
			return fmt.Errorf("%s must be ${VAR} naming an environment variable, not a literal", strings.Join(p, "."))
		}
	}
	return nil
}

func lookup(raw any, path []string) (any, bool) {
	cur := raw
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[key]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

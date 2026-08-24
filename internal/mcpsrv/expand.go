package mcpsrv

import (
	"fmt"
	"maps"
	"os"
	"reflect"
	"regexp"
	"slices"
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

// segment is one step of a secret's path: a JSON key, or every element of the
// container under the previous key. Which container it is — a list or an object
// — the document decides, not the type, so one template matches both.
type segment struct {
	key  string
	each bool
}

// secretPaths returns the path of every field tagged `mcpsrv:"secret"`.
func secretPaths[C any]() [][]segment {
	return secretFields(reflect.TypeFor[C](), nil, map[reflect.Type]bool{})
}

func secretFields(t reflect.Type, prefix []segment, onPath map[reflect.Type]bool) [][]segment {
	// A type reachable from itself would otherwise recurse forever; the cycle
	// carries no field the first turn has not already produced.
	if onPath[t] {
		return nil
	}
	onPath[t] = true
	defer delete(onPath, t)

	var out [][]segment
	for _, f := range reflect.VisibleFields(t) {
		// Promoted fields come through separately, at the level they occupy in JSON.
		if f.Anonymous {
			continue
		}
		name := jsonName(f)
		if name == "" {
			continue
		}
		path := append(append([]segment{}, prefix...), segment{key: name})

		if f.Tag.Get("mcpsrv") == "secret" {
			out = append(out, path)
			continue
		}
		ft, path := descend(f.Type, path)
		if ft.Kind() == reflect.Struct {
			out = append(out, secretFields(ft, path, onPath)...)
		}
	}
	return out
}

// descend strips container layers off t until something that is not a container
// is left, adding one "each" segment per layer that JSON nests — a pointer nests
// nothing. It strips the class rather than the forms it happened to know: a
// container the walk does not step through is a secret it silently fails to
// check.
func descend(t reflect.Type, path []segment) (reflect.Type, []segment) {
	for {
		switch t.Kind() {
		case reflect.Pointer:
			t = t.Elem()
		case reflect.Slice, reflect.Array, reflect.Map:
			path = append(path, segment{each: true})
			t = t.Elem()
		default:
			return t, path
		}
	}
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
func checkSecrets(raw any, paths [][]segment) error {
	for _, p := range paths {
		if err := checkPath(raw, p, ""); err != nil {
			return err
		}
	}
	return nil
}

// checkPath walks one template into the document, carrying `at` — the place it
// actually reached, indices and keys included, because that is what sends a
// reader to the line of the file.
func checkPath(v any, path []segment, at string) error {
	if len(path) == 0 {
		s, ok := v.(string)
		if !ok {
			return nil // absent or mistyped — the schema has already spoken
		}
		if !secretRef.MatchString(s) {
			return fmt.Errorf("%s must be ${VAR} naming an environment variable, not a literal", at)
		}
		return nil
	}

	seg, rest := path[0], path[1:]
	if !seg.each {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		next, ok := m[seg.key]
		if !ok {
			return nil
		}
		return checkPath(next, rest, join(at, seg.key))
	}

	switch c := v.(type) {
	case []any:
		for i, el := range c {
			if err := checkPath(el, rest, fmt.Sprintf("%s[%d]", at, i)); err != nil {
				return err
			}
		}
	case map[string]any:
		// Sorted, so two literals in one object always name the same one first.
		for _, k := range slices.Sorted(maps.Keys(c)) {
			if err := checkPath(c[k], rest, join(at, k)); err != nil {
				return err
			}
		}
	}
	return nil
}

func join(at, key string) string {
	if at == "" {
		return key
	}
	return at + "." + key
}

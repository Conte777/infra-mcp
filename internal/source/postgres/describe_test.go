package postgres

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The ceiling is written twice — once as the constant the handler refuses by,
// once in the schema the model reads before it calls — and only one of them is
// checked by the compiler.
func TestDescribeSchemaNamesTheCeilingItEnforces(t *testing.T) {
	field, ok := reflect.TypeFor[describeArgs]().FieldByName("Tables")
	if !ok {
		t.Fatal("describeArgs has no Tables field")
	}

	// The whole phrase, not the number alone: "at most 20" contains "2", so a
	// ceiling lowered to two would leave the schema promising twenty unchecked.
	schema := field.Tag.Get("jsonschema")
	if want := fmt.Sprintf("at most %d", maxTablesDescribed); !strings.Contains(schema, want) {
		t.Errorf("the tables schema %q does not say %q", schema, want)
	}
}

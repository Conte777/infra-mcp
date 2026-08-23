package postgres

import (
	"reflect"
	"strconv"
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

	schema := field.Tag.Get("jsonschema")
	if want := strconv.Itoa(maxTablesDescribed); !strings.Contains(schema, want) {
		t.Errorf("the tables schema %q does not name the ceiling of %s", schema, want)
	}
}

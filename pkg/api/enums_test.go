package api

import (
	"reflect"
	"testing"
)

func TestSchemaStrictnessValidate(t *testing.T) {
	for _, s := range []SchemaStrictness{"", "none", "warning", "error", "repair", "retry"} {
		if err := s.Validate(); err != nil {
			t.Errorf("SchemaStrictness(%q).Validate() = %v, want nil", s, err)
		}
	}
	const wantErr = `invalid schemaStrictness "strict"; want one of: none, warning, error, repair (retry is an alias)`
	if err := SchemaStrictness("strict").Validate(); err == nil || err.Error() != wantErr {
		t.Errorf("SchemaStrictness(strict).Validate() = %v, want %q", err, wantErr)
	}
	if got := SchemaStrictness("retry").Canonical(); got != "repair" {
		t.Errorf("SchemaStrictness(retry).Canonical() = %q, want repair", got)
	}
	if want := []SchemaStrictness{"none", "warning", "error", "repair"}; !reflect.DeepEqual(AllSchemaStrictness(), want) {
		t.Errorf("AllSchemaStrictness() = %v, want %v", AllSchemaStrictness(), want)
	}
}

func TestPermissionModeValid(t *testing.T) {
	if !PermissionMode("").Valid() || !PermissionAcceptEdits.Valid() || PermissionMode("yolo").Valid() {
		t.Error("PermissionMode.Valid() wrong")
	}
}

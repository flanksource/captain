package api

import (
	"reflect"
	"testing"
)

func TestSchemaStrictnessValidate(t *testing.T) {
	for _, s := range []SchemaStrictness{SchemaStrictnessNone, SchemaStrictnessDisabled, SchemaStrictnessWarning, SchemaStrictnessError, SchemaStrictnessRetry} {
		if err := s.Validate(); err != nil {
			t.Errorf("SchemaStrictness(%q).Validate() = %v, want nil", s, err)
		}
	}
	if err := SchemaStrictness("strict").Validate(); err == nil {
		t.Error("SchemaStrictness(strict).Validate() should fail (only none/warning/error/retry)")
	}
	if want := []SchemaStrictness{SchemaStrictnessDisabled, SchemaStrictnessWarning, SchemaStrictnessError, SchemaStrictnessRetry}; !reflect.DeepEqual(AllSchemaStrictness(), want) {
		t.Errorf("AllSchemaStrictness() = %v, want %v", AllSchemaStrictness(), want)
	}
}

func TestPermissionModeValid(t *testing.T) {
	if !PermissionMode("").Valid() || !PermissionAcceptEdits.Valid() || PermissionMode("yolo").Valid() {
		t.Error("PermissionMode.Valid() wrong")
	}
}

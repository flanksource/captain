package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	"sigs.k8s.io/yaml"
)

// applyPatches applies an RFC 6902 JSON Patch, authored as YAML under
// hack/adapterresources/patches, to a captured upstream source. Patches address
// the source as captured (before root flattening), so one patch reaches every
// projection of that definition. A `test` op guards each change: an upstream
// change to the patched node fails generation instead of silently drifting.
func applyPatches(name string, raw []byte) map[string]any {
	if name != "" {
		document, err := os.ReadFile(filepath.Join("hack/adapterresources/patches", name))
		must(err)
		operations, err := yaml.YAMLToJSON(document)
		must(err)
		patch, err := jsonpatch.DecodePatch(operations)
		must(err)
		raw, err = patch.Apply(raw)
		if err != nil {
			panic(fmt.Sprintf("apply %s: %v", name, err))
		}
	}
	var schema map[string]any
	must(json.Unmarshal(raw, &schema))
	return schema
}

package runtimeprofiles

import (
	"embed"
	"fmt"
)

// BuiltinSourceID is the stable source id of the embedded presets, so a stored
// reference to a built-in id survives across releases.
const BuiltinSourceID = "builtin"

//go:embed builtin/presets/*.yaml
var builtinPresetFiles embed.FS

// NewBuiltinSource is the read-only source of the presets captain ships (Edit,
// Plan, Read-only). It holds no profiles. A database or file preset with the
// same name shadows a built-in; the catalog, not this source, applies that.
func NewBuiltinSource() Source {
	source, err := NewEmbeddedSource(builtinPresetFiles, "builtin/presets", SourceInfo{ID: BuiltinSourceID, Label: "Built-in"})
	if err != nil {
		// The directory is embedded at compile time, so this is a build defect.
		panic(fmt.Sprintf("captain built-in presets: %v", err))
	}
	return source
}

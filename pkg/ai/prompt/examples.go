package prompt

import "embed"

// Examples exposes the prompt examples embedded in the Captain binary.
//
//go:embed testdata
var Examples embed.FS

// Builtin exposes the reusable prompts Captain ships, resolvable by name.
//
//go:embed builtin
var Builtin embed.FS

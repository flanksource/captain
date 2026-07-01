package prompt

import "embed"

// Examples exposes the prompt examples embedded in the Captain binary.
//
//go:embed testdata
var Examples embed.FS

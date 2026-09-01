// Package webapp carries the built Vite assets for `captain serve` and nothing
// else.
//
// A package's compiled object contains its embedded bytes, so while this ~12 MB
// payload lived in pkg/cli — 253 Go files — every edit to any of them rewrote
// the whole payload into a new build-cache entry. Isolating it here means the
// assets are only rewritten when the assets actually change.
package webapp

import "embed"

// The built webapp under dist is generated locally and gitignored. The tracked
// .gitkeep lets the embed pattern compile before `task www:build` generates the
// Vite assets; all: ensures the placeholder is embedded too. When index.html is
// absent, serve.go reports it at runtime.
//
//go:embed all:dist
var FS embed.FS

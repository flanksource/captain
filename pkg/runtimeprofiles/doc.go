// Package runtimeprofiles is the catalog of reusable runtime presets and the
// profiles that compose them, unified across the database and any number of
// directories of YAML files (one record per file, id = file name stem).
//
// A record is addressed by its encoded id (kind, source, key) or by a bare name
// that must resolve to exactly one record across every source. Names are unique
// case-insensitively across sources. A profile's preset references are an
// ordered list of ids or names; deleting a preset a profile still names is
// refused. Layers returns structurally validated raw configuration; Resolve
// materialises a complete profile through api.ResolveSpecLayers for preview.
package runtimeprofiles

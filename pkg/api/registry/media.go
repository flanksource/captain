package registry

import (
	"slices"
	"strings"
)

// MediaTypesFor returns the attachment types a model accepts on a mode: the
// model's own catalog types intersected with the adapter's ceiling. An adapter
// cannot carry what it cannot send, and a model cannot read what it does not
// understand, so the answer is the intersection of the two.
func (p *Provider) MediaTypesFor(mode RuntimeMode, model string) []string {
	caps, ok := p.Caps(mode)
	if !ok {
		return []string{}
	}
	modelTypes := caps.MediaTypes
	if m, found := p.Lookup(model); found && len(m.InputMediaTypes) > 0 {
		modelTypes = m.InputMediaTypes
	}
	return ClampMediaTypes(caps.MediaTypes, modelTypes)
}

// AdapterMediaTypes returns a mode's attachment ceiling.
func (p *Provider) AdapterMediaTypes(mode RuntimeMode) []string {
	caps, ok := p.Caps(mode)
	if !ok {
		return []string{}
	}
	return append([]string{}, caps.MediaTypes...)
}

// ClampMediaTypes intersects a model's declared media types with an adapter's
// supported set, expanding wildcards on either side.
func ClampMediaTypes(adapterTypes, modelTypes []string) []string {
	if len(adapterTypes) == 0 || len(modelTypes) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(modelTypes))
	for _, modelType := range modelTypes {
		modelType = NormalizeMediaType(modelType)
		if modelType == "" {
			continue
		}
		for _, adapterType := range adapterTypes {
			mediaType, ok := intersectMediaTypes(modelType, adapterType)
			if ok && !slices.Contains(out, mediaType) {
				out = append(out, mediaType)
			}
		}
	}
	return out
}

func intersectMediaTypes(modelType, adapterType string) (string, bool) {
	if modelType == adapterType {
		return modelType, true
	}
	if strings.HasSuffix(modelType, "/*") && MediaTypeAccepted([]string{modelType}, adapterType) {
		return adapterType, true
	}
	if strings.HasSuffix(adapterType, "/*") && MediaTypeAccepted([]string{adapterType}, modelType) {
		return modelType, true
	}
	return "", false
}

// NormalizeMediaType canonicalizes the shorthand forms the catalog uses.
// text/* normalizes to empty: prompt text is not an attachment.
func NormalizeMediaType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "image", "image/*":
		return "image/*"
	case "audio", "audio/*":
		return "audio/*"
	case "video", "video/*":
		return "video/*"
	case "pdf", "application/pdf":
		return "application/pdf"
	case "text", "text/*":
		return ""
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

// MediaTypeAccepted reports whether a media type matches any accepted pattern,
// honouring "type/*" wildcards.
func MediaTypeAccepted(accepted []string, mediaType string) bool {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	for _, pattern := range accepted {
		if pattern == mediaType || strings.HasSuffix(pattern, "/*") && strings.HasPrefix(mediaType, strings.TrimSuffix(pattern, "*")) {
			return true
		}
	}
	return false
}

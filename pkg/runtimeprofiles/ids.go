package runtimeprofiles

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// RecordRef is a decoded catalog id: which kind of record, in which source,
// under which source-local key.
type RecordRef struct {
	Kind     Kind
	SourceID string
	Key      string
}

// EncodeID builds the opaque, URL-safe id a record carries across the API.
func EncodeID(kind Kind, sourceID, key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(string(kind) + "\x00" + sourceID + "\x00" + key))
}

// DecodeID is the inverse of EncodeID. Anything that does not decode into a
// known kind plus a non-empty source and key is not an id.
func DecodeID(id string) (RecordRef, error) {
	data, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return RecordRef{}, fmt.Errorf("invalid runtime record id %q: %w", id, err)
	}
	parts := strings.SplitN(string(data), "\x00", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return RecordRef{}, fmt.Errorf("invalid runtime record id %q", id)
	}
	kind := Kind(parts[0])
	if kind != KindPreset && kind != KindProfile {
		return RecordRef{}, fmt.Errorf("invalid runtime record id %q: unknown kind %q", id, parts[0])
	}
	return RecordRef{Kind: kind, SourceID: parts[1], Key: parts[2]}, nil
}

// LooksLikeID reports whether ref is an encoded id rather than a bare name.
func LooksLikeID(ref string) bool {
	_, err := DecodeID(ref)
	return err == nil
}

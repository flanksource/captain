package api

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

const AttachmentIDPrefix = "sha256:"

// AttachmentRef is the serializable reference to one multimodal prompt input.
// Exactly one source is present before resolution; resolved references use ID.
type AttachmentRef struct {
	ID        string `json:"id,omitempty" yaml:"id,omitempty" pretty:"label=ID"`
	Path      string `json:"path,omitempty" yaml:"path,omitempty" pretty:"label=Path"`
	URL       string `json:"url,omitempty" yaml:"url,omitempty" pretty:"label=URL"`
	Filename  string `json:"filename,omitempty" yaml:"filename,omitempty" pretty:"label=Filename"`
	MediaType string `json:"mediaType,omitempty" yaml:"mediaType,omitempty" pretty:"label=Media Type"`
	Size      int64  `json:"size,omitempty" yaml:"size,omitempty" pretty:"label=Size"`
	SHA256    string `json:"sha256,omitempty" yaml:"sha256,omitempty" pretty:"label=SHA-256"`

	preparedBytes []byte
	preparedPath  string
}

// AttachmentContent is runtime-only immutable attachment content.
type AttachmentContent struct {
	Bytes []byte
	Path  string
}

func (a AttachmentRef) Validate() error {
	sources := 0
	for _, source := range []string{a.ID, a.Path, a.URL} {
		if strings.TrimSpace(source) != "" {
			sources++
		}
	}
	if sources != 1 {
		return fmt.Errorf("attachment requires exactly one source: id, path, or url")
	}
	if a.ID != "" {
		if err := validateAttachmentID(a.ID); err != nil {
			return err
		}
	}
	if a.URL != "" {
		u, err := url.Parse(a.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("attachment url must use http or https: %q", a.URL)
		}
	}
	if a.Size < 0 {
		return fmt.Errorf("attachment size cannot be negative")
	}
	if a.SHA256 != "" {
		if err := validateSHA256(a.SHA256); err != nil {
			return err
		}
	}
	return nil
}

func (a AttachmentRef) IsPrepared() bool {
	return a.ID != "" && (len(a.preparedBytes) > 0 || a.preparedPath != "")
}

func (a AttachmentRef) PreparedContent() (AttachmentContent, bool) {
	if !a.IsPrepared() {
		return AttachmentContent{}, false
	}
	return AttachmentContent{Bytes: a.preparedBytes, Path: a.preparedPath}, true
}

func (a AttachmentRef) WithPreparedContent(content AttachmentContent) AttachmentRef {
	a.preparedBytes = content.Bytes
	a.preparedPath = content.Path
	return a
}

func validateAttachmentID(id string) error {
	if !strings.HasPrefix(id, AttachmentIDPrefix) {
		return fmt.Errorf("attachment id must start with %q", AttachmentIDPrefix)
	}
	return validateSHA256(strings.TrimPrefix(id, AttachmentIDPrefix))
}

func validateSHA256(value string) error {
	if len(value) != 64 {
		return fmt.Errorf("attachment sha256 must contain 64 hexadecimal characters")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("attachment sha256 is invalid: %w", err)
	}
	return nil
}

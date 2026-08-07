package session

import (
	"github.com/flanksource/captain/pkg/claude/tools"
	"github.com/segmentio/encoding/json"
)

// Metadata is the monitor-owned dashboard projection stored in
// captain_sessions.metadata — the session facts that are not derivable from the
// turn/message rows. The monitor writes it and the read paths project it back,
// so it lives in pkg/session, the lowest package every one of those importers
// already depends on. Mirroring the shape per-package is what previously made
// todos write-only: the writer emitted a key no reader declared.
type Metadata struct {
	Model     string           `json:"model,omitempty"`
	Provider  string           `json:"provider,omitempty"`
	Files     ChangedFiles     `json:"files,omitempty"`
	Todos     []tools.TodoItem `json:"todos,omitempty"`
	Approvals ApprovalStats    `json:"approvals,omitempty"`
	Plan      *Plan            `json:"plan,omitempty"`
}

// DecodeMetadata projects a stored blob. Rows carry sibling keys from other
// producers (tags, links, legacy_cutover), and rows written before a field
// existed carry nothing for it, so anything unreadable decodes to the zero
// projection rather than failing the read.
func DecodeMetadata(raw json.RawMessage) Metadata {
	var metadata Metadata
	if len(raw) == 0 {
		return metadata
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return Metadata{}
	}
	return metadata
}

// DecodeGitState projects the stored captain_sessions.git blob. Like the
// metadata blob it is best-effort: an unreadable value yields no git state
// rather than failing the session read.
func DecodeGitState(raw json.RawMessage) GitState {
	var git GitState
	if len(raw) == 0 {
		return git
	}
	if err := json.Unmarshal(raw, &git); err != nil {
		return GitState{}
	}
	return git
}

// Encode renders the projection for storage, returning nil when the session
// produced none of it. Storage merges the result (metadata || ?::jsonb) rather
// than replacing the column, so a nil return leaves the stored blob — and the
// sibling keys it carries — untouched. Emptiness is checked per field because
// ChangedFiles and ApprovalStats are structs, which omitempty cannot drop.
func (m Metadata) Encode() map[string]any {
	encoded := map[string]any{}
	if m.Model != "" {
		encoded["model"] = m.Model
	}
	if m.Provider != "" {
		encoded["provider"] = m.Provider
	}
	if len(m.Files.Read) > 0 || len(m.Files.Written) > 0 {
		encoded["files"] = m.Files
	}
	if len(m.Todos) > 0 {
		encoded["todos"] = m.Todos
	}
	if m.Approvals.Approved > 0 || m.Approvals.Denied > 0 {
		encoded["approvals"] = m.Approvals
	}
	if m.Plan != nil {
		encoded["plan"] = m.Plan
	}
	if len(encoded) == 0 {
		return nil
	}
	return encoded
}

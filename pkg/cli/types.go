package cli

import (
	"github.com/flanksource/clicky/api"
)

// ScanResultRow is used when --all flag is set (shows Project column)
type ScanResultRow struct {
	Project         string          `json:"project"`
	Tool            string          `json:"tool"`
	Summary         string          `json:"summary"`
	Subject         api.Textable    `json:"-"`
	Detail          api.Textable    `json:"-"`
	Paths           string          `json:"paths,omitempty"`
	ReadPaths       []string        `json:"readPaths,omitempty"`
	WritePaths      []string        `json:"writePaths,omitempty"`
	BinariesDisplay string          `json:"binariesDisplay,omitempty"`
	Binaries        []string        `json:"binaries,omitempty"`
	Category        string          `json:"category"`
	Approved        string          `json:"approved,omitempty"`
	Time            string          `json:"time"`
}

func (r ScanResultRow) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("time").Label("Time").Build(),
		api.Column("project").Label("Project").Build(),
		api.Column("tool").Label("Tool").Build(),
		api.Column("subject").Label("Subject").Build(),
		api.Column("category").Label("Category").Build(),
		api.Column("approved").Label("Approved").Build(),
	}
}

func (r ScanResultRow) Row() map[string]any {
	return map[string]any{
		"time":     r.Time,
		"project":  r.Project,
		"tool":     r.Tool,
		"subject":  r.Subject,
		"category": r.Category,
		"approved": r.Approved,
	}
}

func (r ScanResultRow) RowDetail() api.Textable {
	return r.Detail
}

// ScanResultRowSingle is used for single project (no Project column)
type ScanResultRowSingle struct {
	Tool            string          `json:"tool"`
	Summary         string          `json:"summary"`
	Subject         api.Textable    `json:"-"`
	Detail          api.Textable    `json:"-"`
	Paths           string          `json:"paths,omitempty"`
	ReadPaths       []string        `json:"readPaths,omitempty"`
	WritePaths      []string        `json:"writePaths,omitempty"`
	BinariesDisplay string          `json:"binariesDisplay,omitempty"`
	Binaries        []string        `json:"binaries,omitempty"`
	Category        string          `json:"category"`
	Approved        string          `json:"approved,omitempty"`
	Time            string          `json:"time"`
}

func (r ScanResultRowSingle) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("time").Label("Time").Build(),
		api.Column("tool").Label("Tool").Build(),
		api.Column("subject").Label("Subject").Build(),
		api.Column("category").Label("Category").Build(),
		api.Column("approved").Label("Approved").Build(),
	}
}

func (r ScanResultRowSingle) Row() map[string]any {
	return map[string]any{
		"time":     r.Time,
		"tool":     r.Tool,
		"subject":  r.Subject,
		"category": r.Category,
		"approved": r.Approved,
	}
}

func (r ScanResultRowSingle) RowDetail() api.Textable {
	return r.Detail
}

// HistoryResultAll is used when --all flag is set
type HistoryResultAll struct {
	Total        int             `json:"total" pretty:"label=Total"`
	UserDenied   int             `json:"userDenied,omitempty" pretty:"label=User Denied"`
	Duration     string          `json:"duration,omitempty" pretty:"label=Duration"`
	Tokens       int             `json:"tokens,omitempty" pretty:"label=Tokens"`
	InputTokens  string          `json:"inputTokens,omitempty" pretty:"label=Input"`
	OutputTokens string          `json:"outputTokens,omitempty" pretty:"label=Output"`
	CacheRead    string          `json:"cacheRead,omitempty" pretty:"label=Cache Read"`
	CacheWrite   string          `json:"cacheWrite,omitempty" pretty:"label=Cache Write"`
	Cost         string          `json:"cost,omitempty" pretty:"label=Cost"`
	Results      []ScanResultRow `json:"results"`
}

// HistoryResult is used for single project view
type HistoryResult struct {
	Project      string                `json:"project" pretty:"label=Project"`
	Total        int                   `json:"total" pretty:"label=Total"`
	UserDenied   int                   `json:"userDenied,omitempty" pretty:"label=User Denied"`
	Duration     string                `json:"duration,omitempty" pretty:"label=Duration"`
	Tokens       int                   `json:"tokens,omitempty" pretty:"label=Tokens"`
	InputTokens  string                `json:"inputTokens,omitempty" pretty:"label=Input"`
	OutputTokens string                `json:"outputTokens,omitempty" pretty:"label=Output"`
	CacheRead    string                `json:"cacheRead,omitempty" pretty:"label=Cache Read"`
	CacheWrite   string                `json:"cacheWrite,omitempty" pretty:"label=Cache Write"`
	Cost         string                `json:"cost,omitempty" pretty:"label=Cost"`
	Results      []ScanResultRowSingle `json:"results"`
}

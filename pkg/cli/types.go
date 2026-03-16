package cli

import (
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/clicky/api"
)

// ScanResultRow is used when --all flag is set (shows Project column)
type ScanResultRow struct {
	Project         string          `json:"project" pretty:"label=Project,table"`
	Tool            string          `json:"tool" pretty:"label=Tool,table"`
	Subject         api.Textable    `json:"subject" pretty:"label=Subject,width=80,table"`
	Paths           string          `json:"paths,omitempty" pretty:"label=Paths,table"`
	ReadPaths       []string        `json:"readPaths,omitempty" pretty:"-"`
	WritePaths      []string        `json:"writePaths,omitempty" pretty:"-"`
	BinariesDisplay string          `json:"binariesDisplay,omitempty" pretty:"label=Binaries,table"`
	Binaries        []string        `json:"binaries,omitempty" pretty:"-"`
	Category        string          `json:"category" pretty:"label=Category,table"`
	Approved        string          `json:"approved,omitempty" pretty:"label=Approved,table"`
	Time            string          `json:"time" pretty:"label=Time,table"`
	ToolUse         *claude.ToolUse `json:"toolUse,omitempty" pretty:"-"`
}

// ScanResultRowSingle is used for single project (no Project column)
type ScanResultRowSingle struct {
	Tool            string          `json:"tool" pretty:"label=Tool,table"`
	Subject         api.Textable    `json:"subject" pretty:"label=Subject,width=80,table"`
	Paths           string          `json:"paths,omitempty" pretty:"label=Paths,table"`
	ReadPaths       []string        `json:"readPaths,omitempty" pretty:"-"`
	WritePaths      []string        `json:"writePaths,omitempty" pretty:"-"`
	BinariesDisplay string          `json:"binariesDisplay,omitempty" pretty:"label=Binaries,table"`
	Binaries        []string        `json:"binaries,omitempty" pretty:"-"`
	Category        string          `json:"category" pretty:"label=Category,table"`
	Approved        string          `json:"approved,omitempty" pretty:"label=Approved,table"`
	Time            string          `json:"time" pretty:"label=Time,table"`
	ToolUse         *claude.ToolUse `json:"toolUse,omitempty" pretty:"-"`
}

// HistoryResultAll is used when --all flag is set
type HistoryResultAll struct {
	Total      int             `json:"total" pretty:"label=Total"`
	UserDenied int             `json:"userDenied,omitempty" pretty:"label=User Denied"`
	Duration   string          `json:"duration,omitempty" pretty:"label=Duration"`
	Tokens     int             `json:"tokens,omitempty" pretty:"label=Tokens"`
	Cost       string          `json:"cost,omitempty" pretty:"label=Cost"`
	Results    []ScanResultRow `json:"results"`
}

// HistoryResult is used for single project view
type HistoryResult struct {
	Project    string                `json:"project" pretty:"label=Project"`
	Total      int                   `json:"total" pretty:"label=Total"`
	UserDenied int                   `json:"userDenied,omitempty" pretty:"label=User Denied"`
	Duration   string                `json:"duration,omitempty" pretty:"label=Duration"`
	Tokens     int                   `json:"tokens,omitempty" pretty:"label=Tokens"`
	Cost       string                `json:"cost,omitempty" pretty:"label=Cost"`
	Results    []ScanResultRowSingle `json:"results"`
}

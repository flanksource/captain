package cli

import (
	"encoding/json"

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
	Cost            string          `json:"cost,omitempty"`
	Raw             json.RawMessage `json:"-"` // surfaced via MarshalJSON when --raw is set
}

func (r ScanResultRow) MarshalJSON() ([]byte, error) {
	type alias ScanResultRow
	return mergeRowWithRaw(alias(r), r.Raw)
}

func (r ScanResultRow) Columns() []api.ColumnDef {
	cols := []api.ColumnDef{
		api.Column("time").Label("Time").Build(),
		api.Column("project").Label("Project").Build(),
		api.Column("tool").Label("Tool").Build(),
		api.Column("subject").Label("Subject").Build(),
		api.Column("category").Label("Category").Build(),
		api.Column("approved").Label("Approved").Build(),
	}
	if r.Cost != "" {
		cols = append(cols, api.Column("cost").Label("Cost").Build())
	}
	return cols
}

func (r ScanResultRow) Row() map[string]any {
	return map[string]any{
		"time":     r.Time,
		"project":  r.Project,
		"tool":     r.Tool,
		"subject":  r.Subject,
		"category": r.Category,
		"approved": r.Approved,
		"cost":     r.Cost,
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
	Cost            string          `json:"cost,omitempty"`
	Raw             json.RawMessage `json:"-"` // surfaced via MarshalJSON when --raw is set
}

func (r ScanResultRowSingle) MarshalJSON() ([]byte, error) {
	type alias ScanResultRowSingle
	return mergeRowWithRaw(alias(r), r.Raw)
}

// mergeRowWithRaw marshals the row's standard fields, then if raw is non-empty
// merges its top-level keys into the resulting JSON object. Standard row
// fields take precedence on conflict so the row's category/tool/etc. don't
// get overwritten by raw fields with the same name.
func mergeRowWithRaw(row any, raw json.RawMessage) ([]byte, error) {
	rowJSON, err := json.Marshal(row)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return rowJSON, nil
	}
	var rowMap map[string]json.RawMessage
	if err := json.Unmarshal(rowJSON, &rowMap); err != nil {
		return rowJSON, nil
	}
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		// Raw isn't a JSON object — surface it under a "raw" key.
		rowMap["raw"] = raw
		return json.Marshal(rowMap)
	}
	for k, v := range rawMap {
		if _, exists := rowMap[k]; !exists {
			rowMap[k] = v
		}
	}
	return json.Marshal(rowMap)
}

func (r ScanResultRowSingle) Columns() []api.ColumnDef {
	cols := []api.ColumnDef{
		api.Column("time").Label("Time").Build(),
		api.Column("tool").Label("Tool").Build(),
		api.Column("subject").Label("Subject").Build(),
		api.Column("category").Label("Category").Build(),
		api.Column("approved").Label("Approved").Build(),
	}
	if r.Cost != "" {
		cols = append(cols, api.Column("cost").Label("Cost").Build())
	}
	return cols
}

func (r ScanResultRowSingle) Row() map[string]any {
	return map[string]any{
		"time":     r.Time,
		"tool":     r.Tool,
		"subject":  r.Subject,
		"category": r.Category,
		"approved": r.Approved,
		"cost":     r.Cost,
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

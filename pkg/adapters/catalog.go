package adapters

import (
	"embed"
	"encoding/json"
	"fmt"

	"github.com/flanksource/captain/pkg/api/registry"
)

//go:embed */*.schema.json
var schemaFiles embed.FS

// SchemaDocument is one native runtime contract exposed to schema viewers.
type SchemaDocument struct {
	Provider    string          `json:"provider"`
	Mode        string          `json:"mode"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

// Schemas returns every non-cmux native runtime schema in canonical registry order.
func Schemas() ([]SchemaDocument, error) {
	var documents []SchemaDocument
	for _, provider := range registry.Providers() {
		for _, mode := range provider.Modes() {
			if mode == registry.ModeCmux {
				continue
			}
			path := fmt.Sprintf("%s/%s.schema.json", provider.Name, mode)
			document, err := readSchemaDocument(path)
			if err != nil {
				return nil, err
			}
			documents = append(documents, document)
		}
	}
	return documents, nil
}

func readSchemaDocument(path string) (SchemaDocument, error) {
	raw, err := schemaFiles.ReadFile(path)
	if err != nil {
		return SchemaDocument{}, fmt.Errorf("read adapter schema %s: %w", path, err)
	}
	var metadata struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Adapter     struct {
			Provider string `json:"provider"`
			Mode     string `json:"mode"`
		} `json:"x-captain-adapter"`
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return SchemaDocument{}, fmt.Errorf("decode adapter schema %s: %w", path, err)
	}
	if metadata.Title == "" || metadata.Description == "" || metadata.Adapter.Provider == "" || metadata.Adapter.Mode == "" {
		return SchemaDocument{}, fmt.Errorf("adapter schema %s has incomplete viewer metadata", path)
	}
	return SchemaDocument{
		Provider: metadata.Adapter.Provider, Mode: metadata.Adapter.Mode,
		Title: metadata.Title, Description: metadata.Description, Schema: raw,
	}, nil
}

package rootcmd

import (
	"encoding/json"
	"fmt"

	"github.com/flanksource/captain/pkg/diagrams"
	"github.com/flanksource/clicky"
	"github.com/spf13/cobra"
)

const standaloneAnnotation = "captain.flanksource.com/standalone"

// MarkStandalone lets a narrowly self-contained command skip Captain's global
// AI, fixture, logging, and database initialization.
func MarkStandalone(cmd *cobra.Command) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[standaloneAnnotation] = "true"
}

// IsStandalone reports whether cmd or one of its parents was marked standalone.
func IsStandalone(cmd *cobra.Command) bool {
	for current := cmd; current != nil; current = current.Parent() {
		if current.Annotations[standaloneAnnotation] == "true" {
			return true
		}
	}
	return false
}

// RegisterDiagramsCommand installs the local filesystem diagram analyzer.
func RegisterDiagramsCommand(root *cobra.Command) {
	diagramsCmd := &cobra.Command{
		Use:   "diagrams",
		Short: "Analyze Facet diagram source",
	}
	clicky.MarkLocalOnly(diagramsCmd)
	root.AddCommand(diagramsCmd)

	var schemaVersion int
	analyzeCmd := &cobra.Command{
		Use:   "analyze --schema-version=1 -- <files...>",
		Short: "Check static BoxNode and Arrow references in explicit source files",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, paths []string) error {
			if !cmd.Flags().Changed("schema-version") {
				return fmt.Errorf("--schema-version is required")
			}
			if schemaVersion != diagrams.SchemaVersion {
				return fmt.Errorf("unsupported --schema-version %d: only version %d is supported", schemaVersion, diagrams.SchemaVersion)
			}
			report, err := diagrams.AnalyzeFiles(paths)
			if err != nil {
				return err
			}
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetEscapeHTML(false)
			if err := encoder.Encode(report); err != nil {
				return fmt.Errorf("write diagram report: %w", err)
			}
			return nil
		},
	}
	analyzeCmd.Flags().IntVar(&schemaVersion, "schema-version", 0, "JSON report schema version (required; supported: 1)")
	MarkStandalone(analyzeCmd)
	diagramsCmd.AddCommand(analyzeCmd)
}

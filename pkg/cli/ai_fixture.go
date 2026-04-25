// ABOUTME: CLI entrypoint for `captain ai fixture`.
// ABOUTME: Loads a YAML fixture, executes it via pkg/ai/fixture, optionally writes markdown.

package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/ai/fixture"
)

type AIFixtureOptions struct {
	File        string `flag:"file" help:"Path to YAML fixture" short:"f" required:"true"`
	Report      string `flag:"report" help:"Write a markdown evidence report to this path" short:"r"`
	ArtifactDir string `flag:"artifacts" help:"Directory to capture per-run stream-json (defaults to <fixture-dir>/.captain/fixtures/<name>)"`
	Repeat      int    `flag:"repeat" help:"Override repeat count for every run (>=1)"`
}

func RunAIFixture(opts AIFixtureOptions) (any, error) {
	f, err := fixture.Load(opts.File)
	if err != nil {
		return nil, err
	}
	if opts.Repeat > 0 {
		f.Repeat = opts.Repeat
	}

	artifactDir := opts.ArtifactDir
	if artifactDir == "" {
		name := f.Name
		if name == "" {
			base := filepath.Base(opts.File)
			name = base[:len(base)-len(filepath.Ext(base))]
		}
		artifactDir = filepath.Join(f.Dir, ".captain", "fixtures", name)
	}

	result, err := fixture.Execute(context.Background(), f, fixture.Options{
		ArtifactDir: artifactDir,
		Progress:    os.Stderr,
	})
	if err != nil {
		return nil, err
	}

	printFindings(os.Stderr, result)

	if opts.Report != "" {
		if err := writeReport(opts.Report, result); err != nil {
			return nil, fmt.Errorf("writing report: %w", err)
		}
	}

	return *result, nil
}

func printFindings(w *os.File, r *fixture.Result) {
	any := false
	for _, row := range r.Rows {
		if row.Result != "" {
			any = true
			break
		}
	}
	if !any {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "── Findings ──")
	for _, row := range r.Rows {
		if row.Result == "" {
			continue
		}
		fmt.Fprintf(w, "\n# %s (%s)\n%s\n", row.Name, row.Model, row.Result)
	}
	fmt.Fprintln(w)
}

func writeReport(path string, r *fixture.Result) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return fixture.WriteMarkdown(file, r)
}

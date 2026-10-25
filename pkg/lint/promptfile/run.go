package promptfile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/clicky"
	commonsContext "github.com/flanksource/commons/context"
	"github.com/flanksource/gavel/linters"
	"github.com/flanksource/gavel/models"
	"github.com/flanksource/gavel/utils"
)

type Options struct {
	WorkDir string
	Paths   []string
	Data    map[string]any
}

func Run(ctx context.Context, options Options) Results {
	linter := &promptfileLinter{options: options}
	result := linters.RunLinterWithTask(ctx, nil, linter, linters.RunOptions{WorkDir: options.WorkDir})
	if result.Violations == nil {
		result.Violations = []models.Violation{}
	}
	return Results{result}
}

type promptfileLinter struct {
	options   Options
	fileCount int
}

var _ linters.Linter = (*promptfileLinter)(nil)

func (*promptfileLinter) Name() string                              { return "promptfile" }
func (*promptfileLinter) DefaultIncludes() []string                 { return []string{"**/*.prompt"} }
func (*promptfileLinter) DefaultExcludes() []string                 { return nil }
func (*promptfileLinter) SupportsJSON() bool                        { return true }
func (*promptfileLinter) JSONArgs() []string                        { return nil }
func (*promptfileLinter) SupportsFix() bool                         { return false }
func (*promptfileLinter) FixArgs() []string                         { return nil }
func (*promptfileLinter) ValidateConfig(*models.LinterConfig) error { return nil }
func (*promptfileLinter) GetRuleCount() int                         { return 8 }
func (l *promptfileLinter) GetFileCount() int                       { return l.fileCount }

func (l *promptfileLinter) Run(ctx commonsContext.Context, _ *clicky.Task) ([]models.Violation, error) {
	workDir := l.options.WorkDir
	if workDir == "" {
		workDir = "."
	}
	root, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}
	files, err := discover(root, l.options.Paths)
	if err != nil {
		return nil, err
	}
	var violations []models.Violation
	var failures []error
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return violations, errors.Join(append(failures, err)...)
		}
		l.fileCount++
		file, err := filepath.Rel(root, path)
		if err != nil {
			return violations, err
		}
		template, loadErr := prompt.LoadFile(path)
		var diagnostics []prompt.Diagnostic
		if loadErr != nil {
			if !hasPromptSymlink(root, path) {
				failures = append(failures, loadErr)
				continue
			}
			diagnostics = []prompt.Diagnostic{prompt.SymlinkDiagnostic(loadErr)}
		} else {
			diagnostics = template.Validate(prompt.ValidationOptions{Data: l.options.Data})
		}
		for _, diagnostic := range diagnostics {
			violation := models.NewViolationBuilder().WithFile(filepath.ToSlash(file)).WithSource(l.Name()).
				WithRuleFromLinter(l.Name(), diagnostic.Rule).WithLocation(diagnostic.Line, diagnostic.Column).
				WithMessage(diagnostic.Message).Build()
			violation.Severity = models.ViolationSeverity(diagnostic.Severity)
			violations = append(violations, violation)
		}
	}
	return violations, errors.Join(failures...)
}

func discover(root string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		paths = []string{"."}
	}
	seen := map[string]bool{}
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("select promptfiles: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			info, err = os.Stat(path)
			if err != nil {
				seen[filepath.Clean(path)] = true
				continue
			}
		}
		if !info.IsDir() {
			if filepath.Ext(path) != ".prompt" {
				return nil, fmt.Errorf("expected a .prompt file: %s", path)
			}
			seen[filepath.Clean(path)] = true
			continue
		}
		matches, err := utils.GlobFilesBounded(path, []string{"**/*.prompt"})
		if err != nil {
			return nil, fmt.Errorf("discover promptfiles in %s: %w", path, err)
		}
		for _, match := range matches {
			seen[match] = true
		}
	}
	files := make([]string, 0, len(seen))
	for file := range seen {
		files = append(files, file)
	}
	if len(files) == 0 {
		return nil, errors.New("no .prompt files matched the selected paths")
	}
	slices.Sort(files)
	return files, nil
}

func hasPromptSymlink(root, path string) bool {
	for {
		info, err := os.Lstat(path)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return true
		}
		parent := filepath.Dir(path)
		if path == root || parent == path {
			return false
		}
		path = parent
	}
}

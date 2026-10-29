package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/clicky"
	clickyapi "github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/formatters"
	"golang.org/x/term"
)

// inputRunBlock is one batched run as it is printed when it completes: its
// status icon and input files (relative to the batch's common directory), then
// its full response, skip reason or error.
func inputRunBlock(item PromptRunItem, base string) clickyapi.Text {
	icon, style := inputRunIcon(item)
	names := make([]string, len(item.Inputs))
	for i, path := range item.Inputs {
		names[i] = relativeInput(path, base)
	}
	block := clickyapi.Text{}.Append(icon+" "+strings.Join(names, ", "), style+" font-bold")
	if item.Duration != "" {
		block = block.Append("  "+item.Duration, "text-gray-500")
	}
	switch {
	case item.Error != "":
		return block.NewLine().Append(stripInputBase(item.Error, base), "text-red-600")
	case item.Status == "skipped":
		return block.Append("  "+stripInputBase(item.SkipReason, base), "text-gray-500")
	case strings.TrimSpace(item.Text) == "":
		return block
	}
	return block.NewLine().Add(promptResponsePretty(item.Text, item.Provider))
}

func inputRunIcon(item PromptRunItem) (string, string) {
	switch {
	case item.Error != "" || item.Status == "failed":
		return "✗", "text-red-600"
	case item.Status == "skipped":
		return "⊘", "text-gray-500"
	}
	return "✓", "text-green-600"
}

// inputBatchTally is a batched run's final print: the per-status counts, the
// files and the directory they share, and the duration. Each run's response was
// already printed as it completed.
func inputBatchTally(r PromptRunResult) clickyapi.Text {
	tally := clickyapi.Text{}
	for _, count := range []struct {
		n           int
		icon, label string
		style       string
	}{
		{r.Succeeded, "✓", "completed", "text-green-600"},
		{r.Failed, "✗", "failed", "text-red-600"},
		{r.Skipped, "⊘", "skipped", "text-gray-500"},
	} {
		if count.n > 0 {
			tally = tally.Append(fmt.Sprintf("%s %d %s  ", count.icon, count.n, count.label), count.style)
		}
	}
	files := 0
	for _, run := range r.Runs {
		files += len(run.Inputs)
	}
	noun := "files"
	if files == 1 {
		noun = "file"
	}
	summary := fmt.Sprintf("·  %d %s", files, noun)
	if base := commonInputDir(r.Runs); base != "" {
		summary += " in " + base
	}
	return tally.Append(summary+"  ·  "+r.Duration, "text-gray-500")
}

// commonInputDir is the deepest directory every run's input files share, or
// "" when they share none.
func commonInputDir(runs []PromptRunItem) string {
	var common []string
	first := true
	for _, run := range runs {
		for _, path := range run.Inputs {
			parts := strings.Split(filepath.Dir(filepath.Clean(path)), string(filepath.Separator))
			if first {
				common, first = parts, false
				continue
			}
			n := 0
			for n < len(common) && n < len(parts) && common[n] == parts[n] {
				n++
			}
			common = common[:n]
		}
	}
	dir := strings.Join(common, string(filepath.Separator))
	if dir == "" && len(common) > 0 {
		return string(filepath.Separator)
	}
	return dir
}

func relativeInput(path, base string) string {
	if base == "" {
		return path
	}
	if rel, err := filepath.Rel(base, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

func stripInputBase(text, base string) string {
	if base == "" || base == string(filepath.Separator) {
		return text
	}
	return strings.ReplaceAll(text, base+string(filepath.Separator), "")
}

// inputBlockPrinter prints each batched run's block through clicky's logger
// output — the task renderer's serializer while tasks run — in the terminal
// format the command prints in. A machine format or a file sink streams
// nothing: the command's final result carries every run instead.
func inputBlockPrinter(opts clicky.FormatOptions) (func(PromptRunItem, string), error) {
	format, err := terminalFormat(opts)
	if err != nil || format == "" {
		return nil, err
	}
	noColor := opts.NoColor || formatters.IsNoColor() || !term.IsTerminal(int(os.Stderr.Fd()))
	return func(item PromptRunItem, base string) {
		out, err := clicky.Format(inputRunBlock(item, base), clicky.FormatOptions{Format: format, NoColor: noColor})
		if err != nil {
			panic(fmt.Errorf("render batched run %s as %s: %w", strings.Join(item.Inputs, ", "), format, err))
		}
		clicky.Println(strings.TrimRight(out, "\n"))
	}, nil
}

// terminalFormat is the format a command's human-facing extras (a footer,
// streamed blocks) render in: pretty or markdown when stdout gets one, "" for
// a machine format or when every sink is a file.
func terminalFormat(opts clicky.FormatOptions) (string, error) {
	if err := opts.ParseFormatSpec(); err != nil {
		return "", err
	}
	format := "pretty"
	if len(opts.Sinks) > 0 {
		format = ""
		for _, sink := range opts.Sinks {
			if sink.File == "" {
				format = sink.Format
			}
		}
	}
	if format != "pretty" && format != "markdown" {
		return "", nil
	}
	return format, nil
}

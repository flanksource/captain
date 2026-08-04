package cli

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/session"
	clickyapi "github.com/flanksource/clicky/api"
)

// transcriptNotice records why a rendered transcript is shorter than the
// session's message count. Filter exclusions and window truncation are counted
// separately: --limit 0 restores windowed rows but can never restore rows a
// --tool/--category filter removed, so blaming one cause for the other's rows
// sends the reader chasing a bigger --limit that changes nothing.
type transcriptNotice struct {
	total          int
	filterExcluded int
	windowHidden   int
	filterFlags    string
	windowFlags    string
}

// applyTranscriptWindow filters then windows the transcript, attributing the
// dropped messages to whichever step removed them. total is the session's full
// message count before either step runs.
func applyTranscriptWindow(detail *session.Session, opts SessionGetOptions, total int) (transcriptNotice, error) {
	notice := transcriptNotice{
		total:       total,
		filterFlags: transcriptFilterFlags(opts),
		windowFlags: transcriptWindowFlags(opts),
	}
	before := len(detail.Messages)
	if err := filterSessionTranscript(detail, opts); err != nil {
		return notice, err
	}
	notice.filterExcluded = before - len(detail.Messages)

	before = len(detail.Messages)
	pageSessionTranscript(detail, opts)
	notice.windowHidden = before - len(detail.Messages)
	return notice, nil
}

func (n transcriptNotice) text(shown int) clickyapi.Text {
	clauses := n.clauses(shown)
	if len(clauses) == 0 {
		return clickyapi.Text{}
	}
	text := clickyapi.Text{}.NewLine().Append("  … "+strings.Join(clauses, "; "), "text-amber-600")
	// Only the window is recoverable, and the advice earns its own line so a
	// multi-cause notice does not run past the terminal width and clip it.
	if n.windowHidden > 0 {
		text = text.NewLine().Append("    use --limit 0 for the full transcript", "text-amber-600")
	}
	return text
}

func (n transcriptNotice) clauses(shown int) []string {
	var clauses []string
	if n.filterExcluded > 0 {
		clauses = append(clauses, fmt.Sprintf("%d of %d messages excluded by %s",
			n.filterExcluded, n.total, n.filterFlags))
	}
	switch {
	case n.windowHidden > 0 && len(clauses) > 0:
		clauses = append(clauses, fmt.Sprintf("%d more hidden by %s", n.windowHidden, n.windowFlags))
	case n.windowHidden > 0:
		clauses = append(clauses, fmt.Sprintf("%d of %d messages hidden by %s",
			n.windowHidden, n.total, n.windowFlags))
	case len(clauses) > 0:
		clauses = append(clauses, "none hidden by "+n.windowFlags)
	}
	// The overview's message count and the recorded transcript can disagree when
	// a session was ingested but never fully parsed; report the gap rather than
	// letting it read as a complete transcript.
	if missing := n.total - shown - n.filterExcluded - n.windowHidden; missing > 0 {
		clauses = append(clauses, fmt.Sprintf("%d not present in the recorded transcript", missing))
	}
	return clauses
}

func transcriptFilterFlags(opts SessionGetOptions) string {
	parts := make([]string, 0, len(opts.Tools)+len(opts.Categories))
	for _, tool := range opts.Tools {
		parts = append(parts, "--tool "+quoteFlagValue(tool))
	}
	for _, category := range opts.Categories {
		parts = append(parts, "--category "+quoteFlagValue(category))
	}
	return strings.Join(parts, " ")
}

func transcriptWindowFlags(opts SessionGetOptions) string {
	if opts.Tail > 0 {
		return fmt.Sprintf("--tail %d", opts.Tail)
	}
	if opts.Offset > 0 {
		return fmt.Sprintf("--offset %d --limit %d", opts.Offset, opts.Limit)
	}
	return fmt.Sprintf("--limit %d", opts.Limit)
}

// quoteFlagValue echoes a filter value the way the user had to type it, so
// shell-significant patterns such as !reasoning or B* stay copy-pasteable.
func quoteFlagValue(value string) string {
	if value != "" && strings.IndexFunc(value, flagValueNeedsQuote) < 0 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func flagValueNeedsQuote(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	case r == '-', r == '_', r == '.', r == '/', r == ':':
		return false
	default:
		return true
	}
}

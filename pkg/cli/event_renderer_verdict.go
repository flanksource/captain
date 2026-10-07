package cli

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/clicky"
)

// renderVerdict writes one verify verdict at the output's own width: the
// headline on the first line, and the verifier's output — the failure the next
// turn is about to be told about — beneath it, one line per line so a test
// runner's tables and traces keep their alignment instead of wrapping.
func (r *EventRenderer) renderVerdict(event ai.Event) {
	headline, body, _ := strings.Cut(strings.TrimRight(event.Text, "\n"), "\n")
	icon, style := "✓", "text-green-500 font-medium"
	if event.Kind == ai.EventVerifyFailed {
		icon, style = "✗", "text-red-500 font-medium"
	}
	prefix := clicky.Text(icon+" verify ", style)
	r.write(truncateANSI(prefix.Append(headline, "text-muted").ANSI(), r.width) + "\n")

	shown := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if shown == maxVerdictBodyLines {
			r.write(clicky.Text(fmt.Sprintf("  … %d more lines (full output sent to the agent)",
				countNonBlank(body)-shown), "text-muted").ANSI() + "\n")
			return
		}
		r.write(truncateANSI(line, r.width) + "\n")
		shown++
	}
}

// maxVerdictBodyLines caps how much of a verdict's output is echoed here. The
// feedback the agent receives is deliberately unbounded by this — a check's
// output streams live through the caller's verify Output sink and the whole
// tail reaches the next iteration — so a megabyte-long failure does not have to
// be replayed down the terminal a second time to be acted on.
const maxVerdictBodyLines = 200

func countNonBlank(body string) int {
	n := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

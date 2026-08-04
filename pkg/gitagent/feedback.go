// The feedback wire format (§7): everything the agent sees arrives through
// the sideband, prefixed `remote:` by its client. CR would be eaten as a
// progress-line terminator (R7.1); the JSON summary is one greppable line
// (R7.2); the block is capped with an explicit truncation marker (R7.3); and
// long hooks emit keepalive traffic so intermediaries don't drop the
// connection (R7.4).
package gitagent

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// MaxFeedbackBytes caps the feedback block (R7.3).
const MaxFeedbackBytes = 64 << 10

// maxFindingFeedback bounds one finding's feedback in both the human block
// and the JSON line, so the line stays a line and the block stays cappable.
const maxFindingFeedback = 8 << 10

// FormatFeedback renders the §7 block. fullLogPath, when non-empty, is named
// in the truncation marker so a capped block still points at the whole story.
func FormatFeedback(v TierVerdict, fullLogPath string) (string, error) {
	v = boundFindings(v)
	jsonLine, err := feedbackJSONLine(v)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	verb := strings.ToUpper(string(v.Status))
	fmt.Fprintf(&b, "captain: %s task %s attempt %d (%s)\n\n", verb, v.Task, v.Attempt, v.Tier)
	for _, f := range v.Findings {
		mark := "✗"
		fmt.Fprintf(&b, "%s %s", mark, f.Hook)
		if f.Path != "" {
			fmt.Fprintf(&b, "   %s", f.Path)
		}
		if f.Message != "" {
			fmt.Fprintf(&b, "   %s", f.Message)
		}
		b.WriteByte('\n')
		for _, line := range strings.Split(strings.TrimSpace(f.Feedback), "\n") {
			if line != "" {
				fmt.Fprintf(&b, "    %s\n", line)
			}
		}
	}
	human := sanitizeCR(b.String())
	budget := MaxFeedbackBytes - len(jsonLine) - 256 // headroom for the marker
	if budget < 0 {
		budget = 0
	}
	if len(human) > budget {
		marker := "\n[feedback truncated"
		if fullLogPath != "" {
			marker += "; full log: " + fullLogPath
		}
		marker += "]\n"
		human = human[:budget] + marker
	}
	return human + "\n" + jsonLine + "\n", nil
}

// boundFindings truncates each finding's feedback to maxFindingFeedback,
// copying so the caller's verdict (and the persisted verdict.json) keeps the
// full text.
func boundFindings(v TierVerdict) TierVerdict {
	if len(v.Findings) == 0 {
		return v
	}
	findings := make([]Finding, len(v.Findings))
	copy(findings, v.Findings)
	for i := range findings {
		if len(findings[i].Feedback) > maxFindingFeedback {
			findings[i].Feedback = findings[i].Feedback[:maxFindingFeedback] + "\n[finding feedback truncated]"
		}
	}
	v.Findings = findings
	return v
}

// feedbackJSONLine renders the single-line machine summary (R7.2). When the
// full form would eat the 64 KiB budget, the summary degrades — feedback
// bodies drop out and the findings list is capped — because the human block
// and the retained log carry the detail; the line must stay recoverable.
func feedbackJSONLine(v TierVerdict) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if len(data) > MaxFeedbackBytes/2 {
		slim := v
		findings := slim.Findings
		if len(findings) > 32 {
			findings = findings[:32]
		}
		slimFindings := make([]Finding, len(findings))
		copy(slimFindings, findings)
		for i := range slimFindings {
			slimFindings[i].Feedback = ""
		}
		slim.Findings = slimFindings
		if data, err = json.Marshal(slim); err != nil {
			return "", err
		}
	}
	return "captain-json: " + sanitizeCR(string(data)), nil
}

// sanitizeCR strips carriage returns: the sideband demuxer consumes CR as a
// progress-line terminator and the text after it is lost (R7.1).
func sanitizeCR(s string) string {
	return strings.ReplaceAll(s, "\r", "")
}

// WriteFeedback renders v onto w (pre-receive stderr → the pusher's sideband).
func WriteFeedback(w io.Writer, v TierVerdict, fullLogPath string) error {
	block, err := FormatFeedback(v, fullLogPath)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, block)
	return err
}

// StartProgress emits a keepalive line to w every interval until the returned
// stop function is called (R7.4). Without sideband traffic during a long
// hook, intermediaries drop the connection and the agent sees a transport
// error instead of a verdict.
func StartProgress(w io.Writer, label string, interval time.Duration) (stop func()) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		start := time.Now()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				fmt.Fprintf(w, "captain: %s still running (%ds)\n", label, int(time.Since(start).Seconds()))
			}
		}
	}()
	return func() { close(done) }
}

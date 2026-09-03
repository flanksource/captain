package verify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
)

// maxReportLineBytes bounds one NDJSON line from a fixture runner. A report is
// a tree of every test that ran, so the bound is generous; without one a runner
// that streams unterminated output buys unbounded memory in the process waiting
// on its verdict.
const maxReportLineBytes = 4 << 20

// ExternalVerifier runs a fixture document through a separate process — the
// host's fixture runner (gavel, today) — and reads its verdict back as a typed
// report. It is how captain dispatches Verify.Fixture without linking a fixture
// engine: captain owns the contract, the runner owns the fixtures.
//
// The contract is one process, three streams:
//
//   - stdin  — the fixture markdown, verbatim.
//   - argv   — the runner's own command, then `--cwd <cwd>` and one `--changed
//     <path>` per changed file.
//   - stdout — NDJSON: zero or more {"progress": <VerifyReport>} lines while it
//     runs, then exactly one {"report": <VerifyReport>}.
//   - stderr — diagnostics, kept as a tail for the error message.
//
// A runner that emits no report, malformed JSON, or more than one report never
// produces a verdict: it produces an error. A fixture whose tests failed is a
// verdict (runners exit non-zero for that), and the report says so.
type ExternalVerifier struct {
	Command []string // argv of the fixture runner; required
	Fixture string   // the fixture document, handed over on stdin
	Timeout time.Duration
	Env     []string
	Wrap    CommandWrapFunc

	progress func(api.VerifyReport)
}

// SetProgress implements ProgressVerifier: the runner's progress lines are
// forwarded as they arrive.
func (e *ExternalVerifier) SetProgress(fn func(api.VerifyReport)) { e.progress = fn }

func (e *ExternalVerifier) Verify(ctx context.Context, cwd string, changed []string) (Verdict, error) {
	if len(e.Command) == 0 {
		return Verdict{}, fmt.Errorf("fixture verifier: no runner command configured")
	}
	args := append([]string(nil), e.Command[1:]...)
	args = append(args, "--cwd", cwd)
	for _, path := range changed {
		args = append(args, "--changed", path)
	}

	reader := &ndjsonReader{onLine: e.consume}
	stderr := newTailBuffer(defaultFeedbackTail)
	outcome, err := runProcess(ctx, execRequest{
		Cmd: e.Command[0], Args: args, Dir: cwd, Env: e.Env, Wrap: e.Wrap, Timeout: e.Timeout,
		Stdin: e.Fixture, Stdout: reader, Stderr: stderr,
	})
	if err != nil {
		return Verdict{}, err
	}
	reader.close()

	name := strings.Join(e.Command, " ")
	if reader.err != nil {
		return Verdict{}, fmt.Errorf("fixture verifier %s: %w%s", name, reader.err, diagnostics(stderr))
	}
	if outcome.TimedOut {
		return Verdict{}, fmt.Errorf("fixture verifier %s timed out after %s%s",
			name, effectiveTimeout(e.Timeout), diagnostics(stderr))
	}
	if reader.report == nil {
		return Verdict{}, fmt.Errorf("fixture verifier %s exited %d with no report line%s%s",
			name, exitCodeOf(outcome.State), processError(outcome.Err), diagnostics(stderr))
	}
	report := *reader.report
	if err := report.Validate(); err != nil {
		return Verdict{}, fmt.Errorf("fixture verifier %s: %w", name, err)
	}
	return Verdict{OK: report.Passed, Reason: report.Reason, Feedback: report.Feedback, Report: &report}, nil
}

// consume dispatches one decoded NDJSON line: a progress snapshot is forwarded
// live, and the report is kept for the verdict.
func (e *ExternalVerifier) consume(line ndjsonLine) error {
	switch {
	case line.Progress != nil:
		if e.progress != nil {
			e.progress(*line.Progress)
		}
	case line.Report == nil:
		return fmt.Errorf(`a stdout line carried neither "progress" nor "report"`)
	}
	return nil
}

// processError names why the process itself failed. A runner binary that does
// not exist never runs, exits -1 and says nothing on stderr, so "exited -1 with
// no report line" alone points at the protocol when the real fault is the argv:
// exec's own error is the only thing that names the missing or misspelled path.
func processError(err error) string {
	if err == nil {
		return ""
	}
	return ": " + err.Error()
}

// diagnostics appends the runner's stderr tail to an error, when it said
// anything: an external runner that fails is usually explaining why on stderr,
// and an error without it sends the reader looking for a log that scrolled past.
func diagnostics(stderr *tailBuffer) string {
	if tail := stderr.String(); tail != "" {
		return "\n" + tail
	}
	return ""
}

// ndjsonLine is one line of the fixture runner's stdout protocol.
type ndjsonLine struct {
	Progress *api.VerifyReport `json:"progress,omitempty"`
	Report   *api.VerifyReport `json:"report,omitempty"`
}

// ndjsonReader splits the runner's stdout into lines as they stream and decodes
// each one, so a progress line reaches a reader while the runner is still
// working rather than after it exits. The first failure is kept and stops
// further decoding: a runner that has gone off-protocol has no verdict to give.
type ndjsonReader struct {
	onLine func(ndjsonLine) error

	buf    bytes.Buffer
	report *api.VerifyReport
	err    error
}

func (r *ndjsonReader) Write(p []byte) (int, error) {
	if r.err != nil {
		// Keep draining: a writer that stops reading its child's stdout
		// deadlocks the child rather than ending the run.
		return len(p), nil
	}
	r.buf.Write(p)
	for r.err == nil {
		line, err := r.buf.ReadBytes('\n')
		if err != nil {
			// No complete line yet; put the fragment back for the next write.
			r.buf.Write(line)
			if r.buf.Len() > maxReportLineBytes {
				r.err = fmt.Errorf("a stdout line exceeded %d bytes without a newline", maxReportLineBytes)
			}
			break
		}
		r.decode(line)
	}
	return len(p), nil
}

func (r *ndjsonReader) decode(raw []byte) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return
	}
	var line ndjsonLine
	if err := json.Unmarshal(trimmed, &line); err != nil {
		r.err = fmt.Errorf("malformed stdout line %q: %w", truncateLine(trimmed), err)
		return
	}
	if line.Report != nil {
		if r.report != nil {
			r.err = fmt.Errorf("emitted more than one report line")
			return
		}
		r.report = line.Report
	}
	if err := r.onLine(line); err != nil {
		// The raw line is the diagnosis: "carried neither key" without it leaves
		// the reader diffing the runner's whole stdout against the protocol.
		r.err = fmt.Errorf("%w: %s", err, truncateLine(trimmed))
	}
}

// close decodes a final line the runner left unterminated.
func (r *ndjsonReader) close() {
	if r.err != nil || r.buf.Len() == 0 {
		return
	}
	r.decode(r.buf.Bytes())
	r.buf.Reset()
}

func truncateLine(line []byte) string {
	const max = 200
	if len(line) <= max {
		return string(line)
	}
	return string(line[:max]) + "…"
}

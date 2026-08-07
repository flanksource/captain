package gitagent

import (
	"strings"
	"testing"
)

func TestRelayFeedbackWriterPreservesLinesAndSupervisorVerdict(t *testing.T) {
	var dst strings.Builder
	writer := &relayFeedbackWriter{dst: &dst}
	if _, err := writer.Write([]byte("remote: first")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(" line\nremote: captain-json: {\"v\":1,\"task\":\"t-1\",\"attempt\":2,\"status\":\"rejected\",\"tier\":\"supervisor\"}\nremote: tail")); err != nil {
		t.Fatal(err)
	}
	if err := writer.flush(); err != nil {
		t.Fatal(err)
	}
	if got := dst.String(); !strings.Contains(got, "first line\n") || !strings.HasSuffix(got, "tail") {
		t.Fatalf("feedback = %q", got)
	}
	if writer.verdict == nil || writer.verdict.Task != "t-1" || writer.verdict.Attempt != 2 {
		t.Fatalf("verdict = %+v", writer.verdict)
	}
}

func TestRelayFeedbackWriterBoundsUnterminatedLines(t *testing.T) {
	var dst strings.Builder
	writer := &relayFeedbackWriter{dst: &dst}
	if _, err := writer.Write([]byte("remote: " + strings.Repeat("x", MaxFeedbackBytes/2))); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(strings.Repeat("x", MaxFeedbackBytes))); err != nil {
		t.Fatal(err)
	}
	if len(writer.pending) > MaxFeedbackBytes {
		t.Fatalf("pending grew to %d bytes", len(writer.pending))
	}
	if _, err := writer.Write([]byte("discarded\nremote: after\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(strings.Repeat("y", MaxFeedbackBytes+1) + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.flush(); err != nil {
		t.Fatal(err)
	}
	got := dst.String()
	if strings.Count(got, relayFeedbackTruncation) != 1 {
		t.Fatalf("truncation count = %d, feedback = %q", strings.Count(got, relayFeedbackTruncation), got)
	}
	if !strings.Contains(got, "after\n") {
		t.Fatalf("normal line after truncation was lost: %q", got)
	}
	if strings.Contains(got, strings.Repeat("x", 32)) || strings.Contains(got, strings.Repeat("y", 32)) {
		t.Fatalf("oversized line content was forwarded: %q", got)
	}
}

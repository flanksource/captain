package monitor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
)

const (
	codexBenchmarkLineCount     = 10_000
	codexBenchmarkTurns         = 1_111
	codexBenchmarkMessages      = codexBenchmarkTurns * 4
	codexEventHeavyTurns        = 99
	codexEventHeavyBlocks       = 12
	codexEventHeavyMessages     = codexEventHeavyTurns * (2 + codexEventHeavyBlocks*4)
	codexEventHeavyLinesPerTurn = 5 + codexEventHeavyBlocks*8
)

var (
	codexBenchmarkMessageCount int
	codexBenchmarkSessionID    string
	codexBenchmarkToolUseCount int
)

type codexBenchmarkFixture struct {
	data     []byte
	lines    int
	turns    int
	messages int
}

type codexBenchmarkWriter struct {
	transcript bytes.Buffer
	encoder    *json.Encoder
	startedAt  time.Time
	lines      int
}

// BenchmarkCodexIngestInput10000Lines is the stable decision benchmark for the
// DB-free whole-file parser and normalization path used after a live append.
func BenchmarkCodexIngestInput10000Lines(b *testing.B) {
	fixture := syntheticCodexBenchmarkTranscript()
	path := prepareCodexBenchmarkFixture(b, "many-turns-10000", fixture)
	benchmarkCodexIngestInput(b, path, fixture)
}

// BenchmarkCodexIngestInputSizeSeries measures how whole-file ingestion scales
// while preserving the same production-valid record mix at each size.
func BenchmarkCodexIngestInputSizeSeries(b *testing.B) {
	for _, turns := range []int{111, 555, codexBenchmarkTurns, 2_778} {
		fixture := syntheticCodexManyTurnTranscript(turns)
		name := fmt.Sprintf("%d_lines_%d_bytes", fixture.lines, len(fixture.data))
		path := prepareCodexBenchmarkFixture(b, name, fixture)
		b.Run(name, func(b *testing.B) {
			benchmarkCodexIngestInput(b, path, fixture)
		})
	}
}

// BenchmarkCodexIncrementalAppendSizeSeries measures the warm monitor path for
// the same nine-line turn appended after progressively larger settled prefixes.
func BenchmarkCodexIncrementalAppendSizeSeries(b *testing.B) {
	for _, turns := range []int{111, codexBenchmarkTurns, 2_778} {
		fixture := syntheticCodexManyTurnTranscript(turns)
		name := fmt.Sprintf("after_%d_lines_%d_bytes", fixture.lines, len(fixture.data))
		path := prepareCodexBenchmarkFixture(b, "warm-"+name, fixture)
		b.Run(name, func(b *testing.B) {
			benchmarkCodexIncrementalAppend(b, path, fixture)
		})
	}
}

// BenchmarkCodexIngestInputWorkloadShapes10000Lines contrasts turn-heavy and
// event-heavy transcripts without changing the exact line count.
func BenchmarkCodexIngestInputWorkloadShapes10000Lines(b *testing.B) {
	fixtures := []struct {
		name    string
		fixture codexBenchmarkFixture
	}{
		{name: "many_turns_1111_turns_4444_messages", fixture: syntheticCodexBenchmarkTranscript()},
		{name: "event_heavy_99_turns_4950_messages", fixture: syntheticCodexEventHeavyTranscript()},
	}
	for _, benchmark := range fixtures {
		path := prepareCodexBenchmarkFixture(b, benchmark.name, benchmark.fixture)
		b.Run(benchmark.name, func(b *testing.B) {
			benchmarkCodexIngestInput(b, path, benchmark.fixture)
		})
	}
}

// BenchmarkCodexIngestPhases10000Lines isolates the exported parser/build seams
// and the monitor-owned final mapping without database or discovery work.
func BenchmarkCodexIngestPhases10000Lines(b *testing.B) {
	fixture := syntheticCodexBenchmarkTranscript()
	path := prepareCodexBenchmarkFixture(b, "phases-10000", fixture)
	sessions := session.BuildCodex([]string{path})
	if len(sessions) != 1 {
		b.Fatalf("build codex fixture: got %d sessions", len(sessions))
	}
	unified := sessions[0]

	b.Run("read_session_info", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			info, err := history.ReadCodexSessionInfo(path)
			if err != nil {
				b.Fatal(err)
			}
			codexBenchmarkSessionID = info.ID
		}
	})
	b.Run("extract_tool_uses", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(fixture.data)))
		for b.Loop() {
			uses, err := history.ExtractCodexToolUses(path)
			if err != nil {
				b.Fatal(err)
			}
			codexBenchmarkToolUseCount = len(uses)
		}
	})
	b.Run("build_codex_session", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(fixture.data)))
		for b.Loop() {
			sessions := session.BuildCodex([]string{path})
			if len(sessions) != 1 {
				b.Fatalf("got %d sessions", len(sessions))
			}
			codexBenchmarkMessageCount = len(sessions[0].Messages)
		}
	})
	b.Run("unified_ingest_input", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			input := unifiedIngestInput(unified, "codex", transcriptSequence)
			codexBenchmarkMessageCount = len(input.Messages)
		}
	})
}

func benchmarkCodexIngestInput(b *testing.B, path string, fixture codexBenchmarkFixture) {
	b.Helper()
	b.ReportAllocs()
	b.SetBytes(int64(len(fixture.data)))
	for b.Loop() {
		input, err := codexIngestInput(path)
		if err != nil {
			b.Fatal(err)
		}
		codexBenchmarkMessageCount = len(input.Messages)
	}
	b.ReportMetric(float64(len(fixture.data)), "fixture_B")
	b.ReportMetric(float64(fixture.lines), "lines")
	b.ReportMetric(float64(fixture.turns), "turns")
	b.ReportMetric(float64(fixture.messages), "messages")
}

func benchmarkCodexIncrementalAppend(b *testing.B, path string, fixture codexBenchmarkFixture) {
	b.Helper()
	ing := seedCodexBenchmarkCheckpoint(b, path)
	line := fixture.lines
	turn := fixture.turns
	firstAppend := syntheticCodexManyTurnAppend(turn, line)
	b.ReportAllocs()
	b.SetBytes(int64(len(firstAppend)))
	b.ResetTimer()
	for b.Loop() {
		b.StopTimer()
		appendData := syntheticCodexManyTurnAppend(turn, line)
		writer, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := writer.Write(appendData); err != nil {
			_ = writer.Close()
			b.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			b.Fatal(err)
		}

		b.StartTimer()
		file, err := os.Open(path)
		if err != nil {
			b.Fatal(err)
		}
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			b.Fatal(err)
		}
		input, preparation, ignored, err := ing.incrementalCodexIngestInput(file, path, info)
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		b.StopTimer()
		if err != nil {
			b.Fatal(err)
		}
		if ignored || len(input.Messages) != 4 || len(input.Turns) != 1 {
			b.Fatalf("warm append normalized to %d messages and %d turns (ignored=%v), want 4 and 1",
				len(input.Messages), len(input.Turns), ignored)
		}
		recordCodexBenchmarkCheckpoint(ing, path, input, preparation, info)
		codexBenchmarkMessageCount = len(input.Messages)
		line += bytes.Count(appendData, []byte{'\n'})
		turn++
		b.StartTimer()
	}
	b.ReportMetric(9, "append_lines")
	b.ReportMetric(float64(fixture.lines), "prefix_lines")
	b.ReportMetric(float64(len(fixture.data)), "prefix_B")
}

func seedCodexBenchmarkCheckpoint(b *testing.B, path string) *ingestor {
	b.Helper()
	ing := &ingestor{
		sources: map[string]database.SessionSourceState{},
		codex:   map[string]*codexCheckpoint{},
	}
	file, err := os.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		b.Fatal(err)
	}
	input, preparation, ignored, err := ing.incrementalCodexIngestInput(file, path, info)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		b.Fatal(err)
	}
	if ignored {
		b.Fatal("benchmark rollout was ignored")
	}
	recordCodexBenchmarkCheckpoint(ing, path, input, preparation, info)
	return ing
}

func recordCodexBenchmarkCheckpoint(ing *ingestor, path string, input database.IngestTranscriptInput, preparation *codexPreparation, info os.FileInfo) {
	modTime := observedModTime(info)
	ing.recordSourceState(database.SessionSourceState{
		Path: path, SourceKind: "codex", SourceIdentity: input.Source.SourceIdentity,
		ParserVersion: parserVersion, ByteOffset: preparation.offset,
		ObservedSize: info.Size(), ObservedModTime: &modTime,
	})
	ing.commitCodexCheckpoint(path, preparation, info, input.Source.SourceIdentity)
}

func prepareCodexBenchmarkFixture(b *testing.B, name string, fixture codexBenchmarkFixture) string {
	b.Helper()
	if fixture.lines != bytes.Count(fixture.data, []byte{'\n'}) {
		b.Fatalf("fixture records %d lines but contains %d", fixture.lines, bytes.Count(fixture.data, []byte{'\n'}))
	}
	path := filepath.Join(b.TempDir(), fmt.Sprintf("rollout-%s-019f0000-0000-7000-8000-000000000001.jsonl", name))
	if err := os.WriteFile(path, fixture.data, 0o600); err != nil {
		b.Fatalf("write codex fixture: %v", err)
	}
	input, err := codexIngestInput(path)
	if err != nil {
		b.Fatalf("parse codex fixture: %v", err)
	}
	if len(input.Messages) != fixture.messages || len(input.Turns) != fixture.turns {
		b.Fatalf("fixture normalized to %d messages and %d turns, want %d messages and %d turns",
			len(input.Messages), len(input.Turns), fixture.messages, fixture.turns)
	}
	return path
}

func newCodexBenchmarkWriter() *codexBenchmarkWriter {
	w := &codexBenchmarkWriter{startedAt: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)}
	w.encoder = json.NewEncoder(&w.transcript)
	return w
}

func (w *codexBenchmarkWriter) write(eventType string, payload map[string]any) {
	event := map[string]any{
		"timestamp": w.startedAt.Add(time.Duration(w.lines) * time.Millisecond).Format(time.RFC3339Nano),
		"type":      eventType,
		"payload":   payload,
	}
	if err := w.encoder.Encode(event); err != nil {
		panic(err)
	}
	w.lines++
}

func (w *codexBenchmarkWriter) writeSessionMeta() {
	w.write("session_meta", map[string]any{
		"id": "019f0000-0000-7000-8000-000000000001", "cwd": "/repo",
		"cli_version": "0.143.0", "model_provider": "openai",
		"git": map[string]any{"branch": "main", "commit_hash": "3304de9598e486fabe4fb5d5ed64fc3a2c5f59ee"},
	})
}

func (w *codexBenchmarkWriter) fixture(turns, messages int) codexBenchmarkFixture {
	return codexBenchmarkFixture{
		data: w.transcript.Bytes(), lines: w.lines, turns: turns, messages: messages,
	}
}

func syntheticCodexBenchmarkTranscript() codexBenchmarkFixture {
	fixture := syntheticCodexManyTurnTranscript(codexBenchmarkTurns)
	if fixture.lines != codexBenchmarkLineCount || fixture.messages != codexBenchmarkMessages {
		panic(fmt.Sprintf("generated %d lines and %d messages", fixture.lines, fixture.messages))
	}
	return fixture
}

func syntheticCodexManyTurnTranscript(turnCount int) codexBenchmarkFixture {
	w := newCodexBenchmarkWriter()
	w.writeSessionMeta()
	for turn := range turnCount {
		w.writeManyTurn(turn)
	}
	return w.fixture(turnCount, turnCount*4)
}

func syntheticCodexManyTurnAppend(turn, line int) []byte {
	w := newCodexBenchmarkWriter()
	w.lines = line
	w.writeManyTurn(turn)
	return w.transcript.Bytes()
}

func (w *codexBenchmarkWriter) writeManyTurn(turn int) {
	turnID := fmt.Sprintf("turn-%04d", turn)
	callID := fmt.Sprintf("call-%04d", turn)
	w.write("turn_context", map[string]any{
		"turn_id": turnID, "model": "gpt-5.6-sol", "effort": "high",
	})
	w.write("event_msg", map[string]any{
		"type": "task_started", "turn_id": turnID,
	})
	w.write("response_item", map[string]any{
		"type": "message", "role": "user",
		"content": []map[string]any{{"type": "input_text", "text": fmt.Sprintf("Inspect parser behavior for turn %d and report the result.", turn)}},
	})
	w.write("response_item", map[string]any{
		"type":    "reasoning",
		"summary": []map[string]any{{"type": "summary_text", "text": fmt.Sprintf("Tracing the monitor ingestion path for turn %d.", turn)}},
	})
	w.write("response_item", map[string]any{
		"type": "function_call", "name": "exec_command", "call_id": callID,
		"arguments": fmt.Sprintf(`{"cmd":"sed -n '1,20p' /repo/pkg/file_%04d.go"}`, turn),
		"internal_chat_message_metadata_passthrough": map[string]any{"turn_id": turnID},
	})
	w.write("response_item", map[string]any{
		"type": "function_call_output", "call_id": callID,
		"output": fmt.Sprintf("Chunk ID: %04d\nProcess exited with code 0\nFinal output:\npackage monitor", turn),
		"internal_chat_message_metadata_passthrough": map[string]any{"turn_id": turnID},
	})
	w.write("response_item", map[string]any{
		"type": "message", "role": "assistant",
		"content": []map[string]any{{"type": "output_text", "text": fmt.Sprintf("The parser and normalizer completed turn %d.", turn)}},
	})
	w.write("event_msg", map[string]any{
		"type": "token_count", "turn_id": turnID, "info": codexBenchmarkTokenInfo(turn + 1),
	})
	w.write("event_msg", map[string]any{
		"type": "task_complete", "turn_id": turnID, "duration_ms": 850,
	})
}

func syntheticCodexEventHeavyTranscript() codexBenchmarkFixture {
	w := newCodexBenchmarkWriter()
	w.writeSessionMeta()
	for turn := range codexEventHeavyTurns {
		turnID := fmt.Sprintf("dense-turn-%03d", turn)
		w.write("turn_context", map[string]any{
			"turn_id": turnID, "model": "gpt-5.6-sol", "effort": "high",
		})
		w.write("event_msg", map[string]any{"type": "task_started", "turn_id": turnID})
		w.write("response_item", map[string]any{
			"type": "message", "role": "user",
			"content": []map[string]any{{"type": "input_text", "text": fmt.Sprintf("Investigate the parser deeply for turn %d.", turn)}},
		})
		for block := range codexEventHeavyBlocks {
			callID := fmt.Sprintf("dense-call-%03d-%02d", turn, block)
			w.write("response_item", map[string]any{
				"type":    "reasoning",
				"summary": []map[string]any{{"type": "summary_text", "text": fmt.Sprintf("Reasoning through turn %d block %d.", turn, block)}},
			})
			w.write("response_item", map[string]any{
				"type": "function_call", "name": "exec_command", "call_id": callID,
				"arguments": fmt.Sprintf(`{"cmd":"rg -n 'parser' /repo/pkg/file_%03d_%02d.go"}`, turn, block),
				"internal_chat_message_metadata_passthrough": map[string]any{"turn_id": turnID},
			})
			w.write("response_item", map[string]any{
				"type": "function_call_output", "call_id": callID,
				"output": fmt.Sprintf("Chunk ID: %03d%02d\nProcess exited with code 0\nFinal output:\n42: parser", turn, block),
				"internal_chat_message_metadata_passthrough": map[string]any{"turn_id": turnID},
			})
			w.write("response_item", map[string]any{
				"type": "message", "role": "assistant",
				"content": []map[string]any{{"type": "output_text", "text": fmt.Sprintf("Observed turn %d block %d.", turn, block)}},
			})
			sequence := turn*codexEventHeavyBlocks + block + 1
			w.write("event_msg", map[string]any{
				"type": "token_count", "turn_id": turnID, "info": codexBenchmarkTokenInfo(sequence),
			})
			w.write("event_msg", map[string]any{
				"type": "agent_reasoning", "turn_id": turnID,
				"text": fmt.Sprintf("Checking another angle for turn %d block %d.", turn, block),
			})
			w.write("world_state", map[string]any{
				"turn_id": turnID, "full": false,
				"state": map[string]any{"skills": map[string]any{"includeInstructions": block%2 == 0}},
			})
			w.write("event_msg", map[string]any{
				"type": "turn_diff", "turn_id": turnID,
				"unified_diff": fmt.Sprintf("@@ turn %d block %d @@", turn, block),
			})
		}
		w.write("response_item", map[string]any{
			"type": "message", "role": "assistant",
			"content": []map[string]any{{"type": "output_text", "text": fmt.Sprintf("Completed dense turn %d.", turn)}},
		})
		w.write("event_msg", map[string]any{
			"type": "task_complete", "turn_id": turnID, "duration_ms": 12_500,
		})
	}
	fixture := w.fixture(codexEventHeavyTurns, codexEventHeavyMessages)
	wantLines := 1 + codexEventHeavyTurns*codexEventHeavyLinesPerTurn
	if fixture.lines != codexBenchmarkLineCount || fixture.lines != wantLines {
		panic(fmt.Sprintf("generated %d event-heavy lines, want %d", fixture.lines, wantLines))
	}
	return fixture
}

func codexBenchmarkTokenInfo(sequence int) map[string]any {
	return map[string]any{
		"last_token_usage": map[string]any{
			"input_tokens": 180, "cached_input_tokens": 60, "output_tokens": 45,
			"reasoning_output_tokens": 15, "total_tokens": 225,
		},
		"total_token_usage": map[string]any{
			"input_tokens": sequence * 180, "cached_input_tokens": sequence * 60,
			"output_tokens": sequence * 45, "reasoning_output_tokens": sequence * 15,
			"total_tokens": sequence * 225,
		},
		"model_context_window": 200_000,
	}
}

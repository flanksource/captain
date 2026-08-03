// ABOUTME: The Messages API SSE emitter — message_start through message_stop, one frame per delta.
// ABOUTME: Chunked deltas are deliberate: a single-chunk stream would not exercise consumer reassembly.

package anthropicmock

import (
	"context"
	"fmt"
	"net/http"

	"github.com/flanksource/captain/pkg/aimock"
)

// toolInputChunk is how many runes of a tool call's JSON arguments go in each
// input_json_delta. Small enough that any realistic tool input spans several
// frames.
const toolInputChunk = 24

// streamMessage renders respond as the documented Messages API event sequence:
// message_start → per block (content_block_start, content_block_delta…,
// content_block_stop) → message_delta → message_stop.
//
// An error here arrives after the 200 and the first frames are already on the
// wire, so it cannot become a status — the caller journals it instead.
func streamMessage(ctx context.Context, w http.ResponseWriter, model string, respond Respond) error {
	sse, err := aimock.NewSSE(w)
	if err != nil {
		return err
	}

	blocks := respond.blocks()
	// message_start reports the input side of the accounting; the cumulative
	// output count lands in message_delta, so output starts at zero here rather
	// than being counted twice by a consumer that sums both frames.
	start := respond.Usage.wire()
	start.OutputTokens = 0

	if err := sse.Event("message_start", messageStartFrame{
		Type: "message_start",
		Message: startMessage{
			ID:      messageID(model),
			Type:    "message",
			Role:    "assistant",
			Model:   model,
			Content: []contentBlock{},
			Usage:   start,
		},
	}); err != nil {
		return err
	}

	// The real API interleaves pings to hold the connection open. Emitting one
	// proves the consumer tolerates frames that carry no content.
	if err := sse.Event("ping", map[string]string{"type": "ping"}); err != nil {
		return err
	}

	for index, block := range blocks {
		if err := streamBlock(sse, index, block); err != nil {
			return err
		}
	}
	if err := aimock.WaitForCancellation(ctx, respond.HoldOpenAfterContent); err != nil {
		return err
	}

	if err := sse.Event("message_delta", messageDeltaFrame{
		Type:  "message_delta",
		Delta: messageDelta{StopReason: respond.resolvedStopReason()},
		Usage: respond.Usage.wire(),
	}); err != nil {
		return err
	}

	return sse.Event("message_stop", map[string]string{"type": "message_stop"})
}

func streamBlock(sse *aimock.SSE, index int, block contentBlock) error {
	if err := sse.Event("content_block_start", blockStartFrame{
		Type:  "content_block_start",
		Index: index,
		Block: openingBlock(block),
	}); err != nil {
		return err
	}

	for _, delta := range blockDeltas(block) {
		if err := sse.Event("content_block_delta", blockDeltaFrame{
			Type:  "content_block_delta",
			Index: index,
			Delta: delta,
		}); err != nil {
			return err
		}
	}

	return sse.Event("content_block_stop", blockStopFrame{Type: "content_block_stop", Index: index})
}

// openingBlock is the empty shell content_block_start carries: the deltas that
// follow fill it in. Tool calls are the exception — their id and name arrive up
// front because only the arguments stream.
func openingBlock(block contentBlock) map[string]any {
	switch block.Type {
	case "thinking":
		return map[string]any{"type": "thinking", "thinking": "", "signature": ""}
	case "tool_use":
		return map[string]any{"type": "tool_use", "id": block.ID, "name": block.Name, "input": map[string]any{}}
	default:
		return map[string]any{"type": "text", "text": ""}
	}
}

func blockDeltas(block contentBlock) []map[string]any {
	var deltas []map[string]any
	switch block.Type {
	case "thinking":
		for _, chunk := range aimock.ChunkText(block.Thinking) {
			deltas = append(deltas, map[string]any{"type": "thinking_delta", "thinking": chunk})
		}
		// The signature closes a thinking block in one frame — it is an opaque
		// token, not prose, so it is never split.
		deltas = append(deltas, map[string]any{"type": "signature_delta", "signature": block.Signature})
	case "tool_use":
		for _, chunk := range chunkJSON(string(block.Input), toolInputChunk) {
			deltas = append(deltas, map[string]any{"type": "input_json_delta", "partial_json": chunk})
		}
	default:
		for _, chunk := range aimock.ChunkText(block.Text) {
			deltas = append(deltas, map[string]any{"type": "text_delta", "text": chunk})
		}
	}
	return deltas
}

// chunkJSON splits compact JSON into fixed-size pieces on rune boundaries. A
// tool call's arguments only parse once every partial_json fragment is
// concatenated, so emitting several proves the consumer accumulates rather than
// parsing each delta on its own.
func chunkJSON(raw string, size int) []string {
	runes := []rune(raw)
	if len(runes) == 0 {
		return nil
	}
	var chunks []string
	for start := 0; start < len(runes); start += size {
		end := start + size
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
	}
	return chunks
}

func messageID(model string) string { return fmt.Sprintf("msg_mock_%s", model) }

// messageStartFrame opens the stream. Its message mirrors a complete reply but
// with empty content and no stop reason yet.
type messageStartFrame struct {
	Type    string       `json:"type"`
	Message startMessage `json:"message"`
}

// startMessage keeps stop_reason and stop_sequence as pointers so both serialize
// as the null the API sends before the turn has ended, rather than "".
type startMessage struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Model        string         `json:"model"`
	Content      []contentBlock `json:"content"`
	StopReason   *string        `json:"stop_reason"`
	StopSequence *string        `json:"stop_sequence"`
	Usage        wireUsage      `json:"usage"`
}

type blockStartFrame struct {
	Type  string         `json:"type"`
	Index int            `json:"index"`
	Block map[string]any `json:"content_block"`
}

type blockDeltaFrame struct {
	Type  string         `json:"type"`
	Index int            `json:"index"`
	Delta map[string]any `json:"delta"`
}

type blockStopFrame struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

type messageDeltaFrame struct {
	Type  string       `json:"type"`
	Delta messageDelta `json:"delta"`
	Usage wireUsage    `json:"usage"`
}

type messageDelta struct {
	StopReason   string  `json:"stop_reason"`
	StopSequence *string `json:"stop_sequence"`
}

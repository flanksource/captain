package cli

import "net/http"

// PromptRunHandler serves the asynchronous prompt runs this process launched,
// with paths relative to wherever a host mounts it (captain serve mounts it at
// /api/captain/prompt/runs via http.StripPrefix, as gavel can):
//
//   - GET /{runId} answers the run's snapshot: {run: PromptRunFrame, entries,
//     state?, verify?, done, summary?: PromptRunSummary, error?}.
//   - GET /{runId}/stream follows it over SSE: a `run` frame (PromptRunFrame,
//     whose sessionId is followable through SessionHandler once known), then
//     `entry` (session.Message), `state`, `verify` frames, ending in `done` or
//     `error` (PromptRunSummary).
//   - POST /{runId}/stop stops the run; POST /{runId}/message, /interrupt and
//     /permission-mode drive a chat run (see prompt_chat_http.go).
//
// A run is started by the prompt entity's `run` action (RegisterPromptEntity,
// served by the host's clicky rpc executor: POST <prefix>/prompt/run?id=<prompt
// id or bare name> with a PromptRenderRequest body), which answers a
// PromptRunResult whose runId addresses this handler. The host must also run
// StartSessionHost so finished runs are pruned and live chats stop with it.
func PromptRunHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /{runId}/stream", handlePromptRunStream(promptRuns))
	mux.Handle("GET /{runId}", handlePromptRunSnapshot(promptRuns))
	mux.Handle("POST /{runId}/message", handlePromptRunMessage(promptChats))
	mux.Handle("POST /{runId}/interrupt", handlePromptRunControl(promptChats, interruptChat))
	mux.Handle("POST /{runId}/stop", handlePromptRunStop(promptRuns, promptChats))
	mux.Handle("POST /{runId}/permission-mode", handlePromptRunControl(promptChats, switchChatPermissionMode))
	return mux
}

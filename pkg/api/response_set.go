package api

// ResponseSet tracks which provider responses have already contributed usage,
// so a transcript can be accumulated without double-counting.
//
// Agent transcript formats commonly write one record per content block — a
// thinking block, a text block and a tool call each get their own line — and
// repeat the whole usage object on every one of them. Accumulating per record
// therefore counts a single response two or three times: a claude session that
// actually read 3.1M cached tokens reports 4.9M, and its cost inflates in step.
//
// This is the FALLBACK for sources that carry no result record. Where the
// provider reports a result — an invocation summary such as claude's
// stream-json `result` line, or a running total such as codex's
// total_token_usage — read that instead. A reported total is exact; a total
// reconstructed from per-record accumulation can only approach it.
type ResponseSet struct {
	seen map[string]bool
}

// First reports whether a response id has not yet contributed, and marks it as
// having done so. An empty id cannot be correlated to a response, so it counts
// once rather than being dropped.
func (r *ResponseSet) First(id string) bool {
	if id == "" {
		return true
	}
	if r.seen[id] {
		return false
	}
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	r.seen[id] = true
	return true
}

package mcpproxy

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func reqWithBody(method, body string) *http.Request {
	r, _ := http.NewRequest(method, "http://example.com/path", strings.NewReader(body))
	return r
}

func TestPeekJSONRPC_ToolsCall(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mc__query","arguments":{"q":"x"}}}`
	r := reqWithBody(http.MethodPost, body)
	method, tool := peekJSONRPC(r)
	if method != "tools/call" {
		t.Errorf("method = %q, want tools/call", method)
	}
	if tool != "mc__query" {
		t.Errorf("tool = %q, want mc__query", tool)
	}
	// Body must remain intact for downstream forwarding.
	got, _ := io.ReadAll(r.Body)
	if string(got) != body {
		t.Errorf("body after peek = %q, want %q", got, body)
	}
}

func TestPeekJSONRPC_NonToolsMethod(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	r := reqWithBody(http.MethodPost, body)
	method, tool := peekJSONRPC(r)
	if method != "initialize" {
		t.Errorf("method = %q, want initialize", method)
	}
	if tool != "" {
		t.Errorf("tool = %q, want empty for non-tools/call", tool)
	}
}

func TestPeekJSONRPC_NonPostSkipped(t *testing.T) {
	r := reqWithBody(http.MethodGet, `{"method":"x"}`)
	method, tool := peekJSONRPC(r)
	if method != "" || tool != "" {
		t.Errorf("non-POST should skip; got %q/%q", method, tool)
	}
}

func TestPeekJSONRPC_MalformedBodyReturnsEmpty(t *testing.T) {
	body := "not json"
	r := reqWithBody(http.MethodPost, body)
	method, tool := peekJSONRPC(r)
	if method != "" || tool != "" {
		t.Errorf("malformed body should yield empty fields; got %q/%q", method, tool)
	}
	// Body must still be readable after a failed peek.
	got, _ := io.ReadAll(r.Body)
	if string(got) != body {
		t.Errorf("body after failed peek = %q, want %q", got, body)
	}
}

func TestPeekJSONRPC_LargeBodyForwardedIntact(t *testing.T) {
	// Body larger than peekCap exercises the MultiReader path. JSON parsing
	// of the truncated prefix is expected to fail (JSON isn't valid until the
	// closing brace), so we only assert that forwarding stays intact — the
	// peek is best-effort, the proxy must not corrupt the request.
	prefix := `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"big","arguments":{"x":"`
	suffix := `"}}}`
	pad := strings.Repeat("a", peekCap+1024)
	body := prefix + pad + suffix
	r := reqWithBody(http.MethodPost, body)
	_, _ = peekJSONRPC(r)
	got, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("large body not preserved: got %d bytes, want %d", len(got), len(body))
	}
}

func TestPeekJSONRPC_BodyReaderErrorRestoresWhatWasRead(t *testing.T) {
	// Reader that returns the first chunk then errors: simulates a half-drained body.
	chunk := `{"method":"tools/call","params":{"name":"x"}}`
	body := &flakyReadCloser{Reader: bytes.NewReader([]byte(chunk)), erranAfter: int64(len(chunk))}
	r, _ := http.NewRequest(http.MethodPost, "http://example.com/", body)

	method, tool := peekJSONRPC(r)
	// Even with a read error, what we did read is enough to extract the JSON-RPC fields.
	if method != "tools/call" || tool != "x" {
		t.Errorf("got method=%q tool=%q, want tools/call and x", method, tool)
	}
	got, _ := io.ReadAll(r.Body)
	if string(got) != chunk {
		t.Errorf("body after error path = %q, want what was buffered (%q)", got, chunk)
	}
}

// flakyReadCloser returns the underlying reader's bytes, then a fake error
// after erranAfter bytes total.
type flakyReadCloser struct {
	Reader     io.Reader
	read       int64
	erranAfter int64
}

func (f *flakyReadCloser) Read(p []byte) (int, error) {
	if f.read >= f.erranAfter {
		return 0, io.ErrUnexpectedEOF
	}
	n, err := f.Reader.Read(p)
	f.read += int64(n)
	return n, err
}

func (f *flakyReadCloser) Close() error { return nil }

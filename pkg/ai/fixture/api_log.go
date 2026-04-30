// ABOUTME: Parsers for the JSONL files produced by kubeproxy and mcpproxy.
// ABOUTME: Tool calls themselves come from the live stream-json output, not from these.

package fixture

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"time"
)

// scanJSONL streams a JSONL file and feeds each line to decode. decode returns
// (parsed, true) when the line should be appended to the result, or (_, false)
// when it should be skipped. Returns an empty slice when the file is missing
// or unreadable — callers treat that as "no log available."
func scanJSONL[T any](path string, decode func([]byte) (T, bool)) []T {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, jsonlScannerInitialBuf), jsonlScannerMaxBuf)
	var out []T
	for scanner.Scan() {
		v, ok := decode(scanner.Bytes())
		if !ok {
			continue
		}
		out = append(out, v)
	}
	return out
}

// readKubectlAPILog parses a kubeproxy JSONL file into chronological API entries.
func readKubectlAPILog(path string) []KubectlAPIEntry {
	return scanJSONL(path, decodeKubectlEntry)
}

func decodeKubectlEntry(line []byte) (KubectlAPIEntry, bool) {
	var ev struct {
		Type     string    `json:"type"`
		Time     time.Time `json:"time"`
		Method   string    `json:"method"`
		Path     string    `json:"path"`
		Query    string    `json:"query"`
		Status   int       `json:"status"`
		Duration string    `json:"duration"`
		Bytes    int64     `json:"bytes"`
	}
	if err := json.Unmarshal(line, &ev); err != nil || ev.Type != "request" {
		return KubectlAPIEntry{}, false
	}
	return KubectlAPIEntry{
		Time:     ev.Time,
		Method:   ev.Method,
		URL:      joinURL(ev.Path, ev.Query),
		Status:   ev.Status,
		Duration: ev.Duration,
		Bytes:    ev.Bytes,
	}, true
}

// readMCPAPILog parses an mcpproxy JSONL file into chronological API entries.
func readMCPAPILog(path string) []MCPAPIEntry {
	return scanJSONL(path, decodeMCPEntry)
}

func decodeMCPEntry(line []byte) (MCPAPIEntry, bool) {
	var ev struct {
		Type      string    `json:"type"`
		Time      time.Time `json:"time"`
		Server    string    `json:"server"`
		Method    string    `json:"method"`
		Path      string    `json:"path"`
		Query     string    `json:"query"`
		Status    int       `json:"status"`
		Duration  string    `json:"duration"`
		Bytes     int64     `json:"bytes"`
		RPCMethod string    `json:"rpcMethod"`
		Tool      string    `json:"tool"`
	}
	if err := json.Unmarshal(line, &ev); err != nil || ev.Type != "request" {
		return MCPAPIEntry{}, false
	}
	return MCPAPIEntry{
		Time:      ev.Time,
		Server:    ev.Server,
		Method:    ev.Method,
		URL:       joinURL(ev.Path, ev.Query),
		RPCMethod: ev.RPCMethod,
		Tool:      ev.Tool,
		Status:    ev.Status,
		Duration:  ev.Duration,
		Bytes:     ev.Bytes,
	}, true
}

func joinURL(path, query string) string {
	if query == "" {
		return path
	}
	return path + "?" + query
}

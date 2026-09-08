package codexconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

const (
	workspaceWriteTable = "[sandbox_workspace_write]"
	writableRootsKey    = "writable_roots"
)

// EnsureWritableRoots adds paths to [sandbox_workspace_write].writable_roots,
// the allowlist codex's seatbelt sandbox checks before permitting a write. It
// returns the paths it had to add; an empty slice means every path was already
// writable.
//
// Editing is line-level so the user's comments, ordering and formatting survive
// — writable_roots is conventionally a multi-line array with per-entry comments,
// and round-tripping it through a TOML encoder would flatten all of that. The
// current values are still read with go-toml, so detection does not depend on
// how the array happens to be laid out.
func EnsureWritableRoots(paths []string) ([]string, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		data = nil
	} else if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var config struct {
		SandboxWorkspaceWrite struct {
			WritableRoots []string `toml:"writable_roots"`
		} `toml:"sandbox_workspace_write"`
	}
	if len(data) > 0 {
		if err := toml.Unmarshal(data, &config); err != nil {
			return nil, fmt.Errorf("parse %s: %w (fix the file before configuring the captain sandbox paths)", path, err)
		}
	}

	missing := missingRoots(config.SandboxWorkspaceWrite.WritableRoots, paths)
	if len(missing) == 0 {
		return nil, nil
	}

	updated, err := insertWritableRoots(string(data), missing)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("ensure %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	return missing, nil
}

// missingRoots keeps the candidates codex does not already allow. A "~" entry
// is expanded before comparison so a home-relative root already in the file is
// not duplicated as an absolute one.
func missingRoots(existing, candidates []string) []string {
	allowed := make(map[string]bool, len(existing))
	for _, root := range existing {
		allowed[expandHome(root)] = true
	}
	var missing []string
	for _, candidate := range candidates {
		expanded := expandHome(candidate)
		if allowed[expanded] {
			continue
		}
		allowed[expanded] = true
		missing = append(missing, candidate)
	}
	return missing
}

func expandHome(path string) string {
	if !strings.HasPrefix(path, "~") {
		return filepath.Clean(path)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(home, strings.TrimPrefix(path, "~")))
}

// insertWritableRoots adds entries to the writable_roots array, handling the
// four shapes the file can be in: no table, a table without the key, a
// single-line array, and a multi-line array.
func insertWritableRoots(content string, roots []string) (string, error) {
	lines := strings.Split(content, "\n")
	tableStart := indexOfTable(lines, workspaceWriteTable)
	if tableStart < 0 {
		return appendWorkspaceWriteTable(content, roots), nil
	}
	keyLine := indexOfKey(lines, tableStart, writableRootsKey)
	if keyLine < 0 {
		entries := make([]string, 0, len(roots))
		for _, root := range roots {
			entries = append(entries, "  "+tomlString(root)+",")
		}
		block := append([]string{writableRootsKey + " = ["}, append(entries, "]")...)
		return strings.Join(insertAt(lines, tableStart+1, block...), "\n"), nil
	}
	if closing := strings.Index(lines[keyLine], "]"); closing >= 0 {
		lines[keyLine] = extendSingleLineArray(lines[keyLine], closing, roots)
		return strings.Join(lines, "\n"), nil
	}
	closing := indexOfArrayEnd(lines, keyLine)
	if closing < 0 {
		return "", fmt.Errorf("%s array is not closed; fix the file before adding sandbox paths", writableRootsKey)
	}
	// TOML permits the last element to omit its trailing comma, and codex's own
	// default config does exactly that. Appending after it without closing the
	// previous element first produces a file that no longer parses.
	if last := indexOfLastEntry(lines, keyLine, closing); last >= 0 {
		lines[last] = ensureTrailingComma(lines[last])
	}
	indent := arrayEntryIndent(lines, keyLine, closing)
	entries := make([]string, 0, len(roots))
	for _, root := range roots {
		entries = append(entries, indent+tomlString(root)+",")
	}
	return strings.Join(insertAt(lines, closing, entries...), "\n"), nil
}

// extendSingleLineArray splices entries in before the array's closing bracket,
// preserving whatever trails it (codex's default config ends the line with an
// explanatory comment).
func extendSingleLineArray(line string, closing int, roots []string) string {
	head := strings.TrimRight(line[:closing], " \t")
	separator := ", "
	if strings.HasSuffix(head, "[") {
		separator = ""
	} else if strings.HasSuffix(head, ",") {
		separator = " "
	}
	quoted := make([]string, 0, len(roots))
	for _, root := range roots {
		quoted = append(quoted, tomlString(root))
	}
	return head + separator + strings.Join(quoted, ", ") + line[closing:]
}

func appendWorkspaceWriteTable(content string, roots []string) string {
	var builder strings.Builder
	builder.WriteString(content)
	if content != "" && !strings.HasSuffix(content, "\n") {
		builder.WriteString("\n")
	}
	if content != "" {
		builder.WriteString("\n")
	}
	builder.WriteString(workspaceWriteTable + "\n" + writableRootsKey + " = [\n")
	for _, root := range roots {
		builder.WriteString("  " + tomlString(root) + ",\n")
	}
	builder.WriteString("]\n")
	return builder.String()
}

func indexOfTable(lines []string, table string) int {
	for i, line := range lines {
		if strings.TrimSpace(line) == table {
			return i
		}
	}
	return -1
}

// indexOfKey finds an assignment inside the table that starts at tableStart,
// stopping at the next table header.
func indexOfKey(lines []string, tableStart int, key string) int {
	for i := tableStart + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "[") {
			return -1
		}
		rest, ok := strings.CutPrefix(trimmed, key)
		if !ok {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(rest), "=") {
			return i
		}
	}
	return -1
}

// indexOfArrayEnd finds the line closing a multi-line array opened on keyLine.
func indexOfArrayEnd(lines []string, keyLine int) int {
	for i := keyLine + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "]") {
			return i
		}
	}
	return -1
}

// indexOfLastEntry finds the last line carrying an array element, skipping
// blank and comment-only lines.
func indexOfLastEntry(lines []string, keyLine, closing int) int {
	for i := closing - 1; i > keyLine; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		return i
	}
	return -1
}

// ensureTrailingComma appends a comma to an array element that omits one,
// inserting it before any trailing comment rather than after it.
func ensureTrailingComma(line string) string {
	code, comment := splitTOMLComment(line)
	trimmed := strings.TrimRight(code, " \t")
	if trimmed == "" || strings.HasSuffix(trimmed, ",") || strings.HasSuffix(trimmed, "[") {
		return line
	}
	return trimmed + "," + code[len(trimmed):] + comment
}

// splitTOMLComment splits a line into its code and its trailing comment. The
// scan tracks string state so a '#' inside a quoted path is not mistaken for a
// comment marker.
func splitTOMLComment(line string) (string, string) {
	inString := false
	escaped := false
	for i, char := range line {
		switch {
		case escaped:
			escaped = false
		case char == '\\' && inString:
			escaped = true
		case char == '"':
			inString = !inString
		case char == '#' && !inString:
			return line[:i], line[i:]
		}
	}
	return line, ""
}

// arrayEntryIndent copies the indentation of the array's existing entries so an
// inserted line matches the file's style.
func arrayEntryIndent(lines []string, keyLine, closing int) string {
	for i := keyLine + 1; i < closing; i++ {
		trimmed := strings.TrimLeft(lines[i], " \t")
		if trimmed == "" {
			continue
		}
		return lines[i][:len(lines[i])-len(trimmed)]
	}
	return "  "
}

func insertAt(lines []string, index int, values ...string) []string {
	combined := make([]string, 0, len(lines)+len(values))
	combined = append(combined, lines[:index]...)
	combined = append(combined, values...)
	return append(combined, lines[index:]...)
}

// tomlString renders a TOML basic string. A JSON string is valid TOML.
func tomlString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `"` + value + `"`
	}
	return string(encoded)
}

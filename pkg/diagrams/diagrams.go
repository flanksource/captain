// Package diagrams statically checks Facet diagram source files.
package diagrams

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// SchemaVersion is the JSON report schema emitted by this package.
const SchemaVersion = 1

const (
	CodeDuplicateBoxID = "duplicate-box-id"
	CodeMissingFrom    = "missing-arrow-from"
	CodeMissingTo      = "missing-arrow-to"
	CodeDuplicateArrow = "duplicate-arrow"
)

// Report is the stable machine-readable result of an analysis.
type Report struct {
	SchemaVersion    int          `json:"schema_version"`
	FilesAnalyzed    int          `json:"files_analyzed"`
	DiagramsAnalyzed int          `json:"diagrams_analyzed"`
	Diagnostics      []Diagnostic `json:"diagnostics"`
}

// Diagnostic describes one statically proven problem. File is the exact path
// supplied by the caller, and line and column are one-based.
type Diagnostic struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column,omitempty"`
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// AnalyzeFiles analyzes explicit source paths in caller-supplied order.
// Markdown is accepted as a no-op source type; TSX and MDX are checked.
func AnalyzeFiles(paths []string) (Report, error) {
	report := Report{SchemaVersion: SchemaVersion, Diagnostics: []Diagnostic{}}
	if len(paths) == 0 {
		return report, fmt.Errorf("at least one source file is required")
	}

	for _, path := range paths {
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".tsx" && ext != ".mdx" && ext != ".md" {
			return report, fmt.Errorf("unsupported diagram source %q: expected .tsx, .mdx, or .md", path)
		}
		info, err := os.Stat(path)
		if err != nil {
			return report, fmt.Errorf("read diagram source %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return report, fmt.Errorf("diagram source %q is not a regular file", path)
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return report, fmt.Errorf("read diagram source %q: %w", path, err)
		}
		report.FilesAnalyzed++
		if ext == ".md" {
			continue
		}
		diagnostics, diagramsAnalyzed, err := analyzeSource(path, string(source), ext == ".mdx")
		if err != nil {
			return report, fmt.Errorf("analyze diagram source %q: %w", path, err)
		}
		report.DiagramsAnalyzed += diagramsAnalyzed
		report.Diagnostics = append(report.Diagnostics, diagnostics...)
	}
	return report, nil
}

type tag struct {
	name        string
	start       int
	end         int
	closing     bool
	selfClosing bool
}

type diagramBlock struct {
	open  tag
	close tag
}

type finding struct {
	offset     int
	sequence   int
	diagnostic Diagnostic
}

type endpoint struct {
	name   string
	offset int
}

type arrow struct {
	from endpoint
	to   endpoint
}

type attributeCall struct {
	value   endpoint
	present bool
	valid   bool
}

func analyzeSource(path, source string, mdx bool) ([]Diagnostic, int, error) {
	tags := scanTags(source, mdx)
	blocks, err := pairDiagrams(tags)
	if err != nil {
		return nil, 0, err
	}

	var findings []finding
	diagramsAnalyzed := 0
	sequence := 0
	for _, block := range blocks {
		binder, ok := diagramBinder(source, block.open.end, block.close.start)
		if !ok {
			continue
		}
		diagramsAnalyzed++

		boxIDs := map[string]endpoint{}
		arrows := []arrow{}
		for _, current := range tags {
			if current.closing || current.start <= block.open.start || current.start >= block.close.start {
				continue
			}
			if insideNestedDiagram(current.start, block, blocks) {
				continue
			}
			switch current.name {
			case "BoxNode":
				id, recognized := staticAttributeCall(source, current, "id", binder)
				if !recognized {
					continue
				}
				if _, exists := boxIDs[id.name]; exists {
					findings = append(findings, makeFinding(path, source, id.offset, sequence,
						"error", CodeDuplicateBoxID, fmt.Sprintf("duplicate BoxNode id %q in this Diagram", id.name)))
					sequence++
				} else {
					boxIDs[id.name] = id
				}
			case "Arrow":
				from := arrowEndpoint(source, current, "from", "start", binder)
				to := arrowEndpoint(source, current, "to", "end", binder)
				arrows = append(arrows, arrow{
					from: from,
					to:   to,
				})
			}
		}

		seenArrows := map[string]bool{}
		for _, current := range arrows {
			if current.from.offset != 0 {
				if _, exists := boxIDs[current.from.name]; !exists {
					findings = append(findings, makeFinding(path, source, current.from.offset, sequence,
						"error", CodeMissingFrom, fmt.Sprintf("Arrow from endpoint %q has no matching BoxNode id in this Diagram", current.from.name)))
					sequence++
				}
			}
			if current.to.offset != 0 {
				if _, exists := boxIDs[current.to.name]; !exists {
					findings = append(findings, makeFinding(path, source, current.to.offset, sequence,
						"error", CodeMissingTo, fmt.Sprintf("Arrow to endpoint %q has no matching BoxNode id in this Diagram", current.to.name)))
					sequence++
				}
			}
			if current.from.offset != 0 && current.to.offset != 0 {
				key := current.from.name + "\x00" + current.to.name
				if seenArrows[key] {
					findings = append(findings, makeFinding(path, source, current.from.offset, sequence,
						"warning", CodeDuplicateArrow, fmt.Sprintf("duplicate Arrow from %q to %q in this Diagram", current.from.name, current.to.name)))
					sequence++
				} else {
					seenArrows[key] = true
				}
			}
		}
	}

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].offset == findings[j].offset {
			return findings[i].sequence < findings[j].sequence
		}
		return findings[i].offset < findings[j].offset
	})
	diagnostics := make([]Diagnostic, len(findings))
	for i := range findings {
		diagnostics[i] = findings[i].diagnostic
	}
	return diagnostics, diagramsAnalyzed, nil
}

func endpointIf(value endpoint, ok bool) endpoint {
	if !ok {
		return endpoint{}
	}
	return value
}

// arrowEndpoint recognizes Facet's canonical from/to props and retains the
// older start/end names as compatibility aliases. A canonical prop wins even
// when its expression is dynamic, preserving the analyzer's rule that dynamic
// expressions are ignored rather than falling back to a compatibility alias.
func arrowEndpoint(source string, current tag, canonical, alias, binder string) endpoint {
	preferred := staticAttribute(source, current, canonical, binder)
	if preferred.present {
		return endpointIf(preferred.value, preferred.valid)
	}
	fallback := staticAttribute(source, current, alias, binder)
	return endpointIf(fallback.value, fallback.valid)
}

func makeFinding(path, source string, offset, sequence int, severity, code, message string) finding {
	line, column := lineColumn(source, offset)
	return finding{offset: offset, sequence: sequence, diagnostic: Diagnostic{
		File: path, Line: line, Column: column, Severity: severity, Code: code, Message: message,
	}}
}

func lineColumn(source string, offset int) (int, int) {
	line, column := 1, 1
	if offset > len(source) {
		offset = len(source)
	}
	for _, value := range source[:offset] {
		if value == '\n' {
			line, column = line+1, 1
		} else {
			column++
		}
	}
	return line, column
}

func pairDiagrams(tags []tag) ([]diagramBlock, error) {
	var stack []tag
	var blocks []diagramBlock
	for _, current := range tags {
		if current.name != "Diagram" {
			continue
		}
		if current.closing {
			if len(stack) == 0 {
				return nil, fmt.Errorf("closing Diagram has no matching opening tag")
			}
			open := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			blocks = append(blocks, diagramBlock{open: open, close: current})
		} else if !current.selfClosing {
			stack = append(stack, current)
		}
	}
	if len(stack) != 0 {
		return nil, fmt.Errorf("opening Diagram has no matching closing tag")
	}
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].open.start < blocks[j].open.start })
	return blocks, nil
}

func insideNestedDiagram(offset int, parent diagramBlock, blocks []diagramBlock) bool {
	for _, candidate := range blocks {
		if candidate.open.start > parent.open.start && candidate.close.end < parent.close.end &&
			offset >= candidate.open.start && offset <= candidate.close.end {
			return true
		}
	}
	return false
}

func diagramBinder(source string, start, limit int) (string, bool) {
	i := skipSpaceAndComments(source, start, limit)
	if i >= limit || source[i] != '{' {
		return "", false
	}
	i = skipSpaceAndComments(source, i+1, limit)
	parenthesized := i < limit && source[i] == '('
	if parenthesized {
		i = skipSpaceAndComments(source, i+1, limit)
	}
	nameStart := i
	if i >= limit || !isIdentifierStart(source[i]) {
		return "", false
	}
	for i < limit && isIdentifierPart(source[i]) {
		i++
	}
	name := source[nameStart:i]
	i = skipSpaceAndComments(source, i, limit)
	if parenthesized {
		if i >= limit || source[i] != ')' {
			return "", false
		}
		i = skipSpaceAndComments(source, i+1, limit)
	}
	if i+1 >= limit || source[i:i+2] != "=>" {
		return "", false
	}
	return name, true
}

func staticAttributeCall(source string, current tag, attribute, binder string) (endpoint, bool) {
	parsed := staticAttribute(source, current, attribute, binder)
	return parsed.value, parsed.valid
}

func staticAttribute(source string, current tag, attribute, binder string) attributeCall {
	i := current.start + 1 + len(current.name)
	for i < current.end {
		i = skipSpaceAndComments(source, i, current.end)
		if i >= current.end || source[i] == '>' || source[i] == '/' {
			break
		}
		nameStart := i
		for i < current.end && (isIdentifierPart(source[i]) || source[i] == '-' || source[i] == ':') {
			i++
		}
		if nameStart == i {
			i++
			continue
		}
		name := source[nameStart:i]
		i = skipSpaceAndComments(source, i, current.end)
		if i >= current.end || source[i] != '=' {
			if name == attribute {
				return attributeCall{present: true}
			}
			continue
		}
		i = skipSpaceAndComments(source, i+1, current.end)
		if i >= current.end {
			if name == attribute {
				return attributeCall{present: true}
			}
			break
		}
		if source[i] == '\'' || source[i] == '"' {
			if name == attribute {
				return attributeCall{present: true}
			}
			i = skipQuoted(source, i, current.end)
			continue
		}
		if source[i] != '{' {
			if name == attribute {
				return attributeCall{present: true}
			}
			for i < current.end && !unicode.IsSpace(rune(source[i])) && source[i] != '>' && source[i] != '/' {
				i++
			}
			continue
		}
		exprStart := i + 1
		exprEnd, ok := matchingBrace(source, i, current.end)
		if !ok {
			if name == attribute {
				return attributeCall{present: true}
			}
			return attributeCall{}
		}
		i = exprEnd + 1
		if name == attribute {
			value, valid := parseLiteralCall(source, exprStart, exprEnd, binder)
			return attributeCall{value: value, present: true, valid: valid}
		}
	}
	return attributeCall{}
}

func parseLiteralCall(source string, start, end int, binder string) (endpoint, bool) {
	i := skipSpaceAndComments(source, start, end)
	callOffset := i
	if i+len(binder) > end || source[i:i+len(binder)] != binder {
		return endpoint{}, false
	}
	i += len(binder)
	if i < end && isIdentifierPart(source[i]) {
		return endpoint{}, false
	}
	i = skipSpaceAndComments(source, i, end)
	if i >= end || source[i] != '(' {
		return endpoint{}, false
	}
	i = skipSpaceAndComments(source, i+1, end)
	if i >= end || (source[i] != '\'' && source[i] != '"') {
		return endpoint{}, false
	}
	quote := source[i]
	valueStart := i + 1
	i++
	for i < end && source[i] != quote {
		if source[i] == '\\' || source[i] == '\n' || source[i] == '\r' {
			return endpoint{}, false
		}
		i++
	}
	if i >= end {
		return endpoint{}, false
	}
	value := source[valueStart:i]
	i = skipSpaceAndComments(source, i+1, end)
	if i >= end || source[i] != ')' {
		return endpoint{}, false
	}
	i = skipSpaceAndComments(source, i+1, end)
	if i != end {
		return endpoint{}, false
	}
	return endpoint{name: value, offset: callOffset}, true
}

func matchingBrace(source string, start, limit int) (int, bool) {
	depth := 0
	for i := start; i < limit; i++ {
		switch source[i] {
		case '\'', '"', '`':
			i = skipQuoted(source, i, limit) - 1
		case '/':
			if i+1 < limit && source[i+1] == '/' {
				i = skipLineComment(source, i+2, limit) - 1
			} else if i+1 < limit && source[i+1] == '*' {
				i = skipBlockComment(source, i+2, limit) - 1
			}
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

func scanTags(source string, mdx bool) []tag {
	fences := fencedCodeRanges(source, mdx)
	fenceIndex := 0
	afterTag := false
	var tags []tag
	for i := 0; i < len(source); {
		if fenceIndex < len(fences) && i >= fences[fenceIndex][0] {
			if i < fences[fenceIndex][1] {
				i = fences[fenceIndex][1]
				fenceIndex++
				afterTag = false
				continue
			}
			fenceIndex++
			continue
		}
		if mdx && strings.HasPrefix(source[i:], "<!--") {
			if end := strings.Index(source[i+4:], "-->"); end >= 0 {
				i += 4 + end + 3
			} else {
				i = len(source)
			}
			continue
		}
		if source[i] == '/' && i+1 < len(source) {
			if source[i+1] == '/' {
				i = skipLineComment(source, i+2, len(source))
				continue
			}
			if source[i+1] == '*' {
				i = skipBlockComment(source, i+2, len(source))
				continue
			}
			if !afterTag && canStartRegex(source, i) {
				if end, ok := skipRegexLiteral(source, i); ok {
					i = end
					afterTag = false
					continue
				}
			}
		}
		if source[i] == '\'' || source[i] == '"' || source[i] == '`' {
			i = skipQuoted(source, i, len(source))
			afterTag = false
			continue
		}
		if source[i] == '<' {
			if parsed, ok := parseTag(source, i); ok {
				if parsed.name == "Diagram" || parsed.name == "BoxNode" || parsed.name == "Arrow" {
					tags = append(tags, parsed)
				}
				i = parsed.end
				afterTag = true
				continue
			}
		}
		if !unicode.IsSpace(rune(source[i])) {
			afterTag = false
		}
		i++
	}
	return tags
}

func parseTag(source string, start int) (tag, bool) {
	i := start + 1
	closing := false
	if i < len(source) && source[i] == '/' {
		closing = true
		i++
	}
	nameStart := i
	for i < len(source) && (isIdentifierPart(source[i]) || source[i] == '.') {
		i++
	}
	if nameStart == i {
		return tag{}, false
	}
	fullName := source[nameStart:i]
	parts := strings.Split(fullName, ".")
	name := parts[len(parts)-1]
	if i < len(source) && !unicode.IsSpace(rune(source[i])) && source[i] != '>' && source[i] != '/' {
		return tag{}, false
	}

	braceDepth := 0
	for i < len(source) {
		switch source[i] {
		case '\'', '"', '`':
			i = skipQuoted(source, i, len(source))
			continue
		case '{':
			braceDepth++
		case '}':
			if braceDepth > 0 {
				braceDepth--
			}
		case '>':
			if braceDepth == 0 {
				j := i - 1
				for j > start && unicode.IsSpace(rune(source[j])) {
					j--
				}
				return tag{name: name, start: start, end: i + 1, closing: closing, selfClosing: !closing && source[j] == '/'}, true
			}
		}
		i++
	}
	return tag{}, false
}

func fencedCodeRanges(source string, enabled bool) [][2]int {
	if !enabled {
		return nil
	}
	var ranges [][2]int
	lineStart := 0
	openStart := -1
	var marker byte
	markerLength := 0
	for lineStart <= len(source) {
		lineEnd := strings.IndexByte(source[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(source)
		} else {
			lineEnd += lineStart
		}
		trimmed := strings.TrimLeft(source[lineStart:lineEnd], " \t")
		if len(trimmed) >= 3 && (trimmed[0] == '`' || trimmed[0] == '~') {
			count := 0
			for count < len(trimmed) && trimmed[count] == trimmed[0] {
				count++
			}
			if count >= 3 {
				if openStart < 0 {
					openStart, marker, markerLength = lineStart, trimmed[0], count
				} else if trimmed[0] == marker && count >= markerLength && strings.TrimSpace(trimmed[count:]) == "" {
					end := lineEnd
					if end < len(source) {
						end++
					}
					ranges = append(ranges, [2]int{openStart, end})
					openStart = -1
				}
			}
		}
		if lineEnd == len(source) {
			break
		}
		lineStart = lineEnd + 1
	}
	if openStart >= 0 {
		ranges = append(ranges, [2]int{openStart, len(source)})
	}
	return ranges
}

// canStartRegex reports whether a slash at start can begin a regular-expression
// literal. It intentionally recognizes only the unambiguous expression-start
// contexts needed by this scanner; ambiguous cases are left alone so division
// expressions cannot hide valid JSX tags.
func canStartRegex(source string, start int) bool {
	// A slash immediately following '<' is a JSX closing-tag marker, not a
	// regular-expression literal.
	if start > 0 && source[start-1] == '<' {
		return false
	}
	i := start - 1
	for i >= 0 && (source[i] == ' ' || source[i] == '\t' || source[i] == '\n' || source[i] == '\r') {
		i--
	}
	if i < 0 {
		return true
	}

	switch source[i] {
	case '(', '[', '{', ',', ';', ':', '=', '!', '?', '&', '|', '+', '-', '*', '%', '^', '~', '<', '>':
		if (source[i] == '+' || source[i] == '-') && i > 0 && source[i-1] == source[i] {
			return false
		}
		return true
	case ')', ']', '}', '.', '"', '\'':
		return false
	}

	if !isIdentifierPart(source[i]) {
		return false
	}
	end := i + 1
	for i >= 0 && isIdentifierPart(source[i]) {
		i--
	}
	switch source[i+1 : end] {
	case "await", "case", "delete", "do", "else", "in", "instanceof", "new", "of", "return", "throw", "typeof", "void", "yield":
		return true
	default:
		return false
	}
}

// skipRegexLiteral skips a JavaScript regular-expression literal, including
// escaped bytes, character classes, and trailing flags. It does not attempt
// to validate the expression; an unterminated literal is left to the normal
// tag scanner for conservative behavior.
func skipRegexLiteral(source string, start int) (int, bool) {
	inClass := false
	for i := start + 1; i < len(source); i++ {
		switch source[i] {
		case '\\':
			if i+1 >= len(source) {
				return 0, false
			}
			i++
		case '\n', '\r':
			return 0, false
		case '[':
			if !inClass {
				inClass = true
			}
		case ']':
			if inClass {
				inClass = false
			}
		case '/':
			if inClass {
				continue
			}
			i++
			for i < len(source) && isIdentifierPart(source[i]) {
				i++
			}
			return i, true
		}
	}
	return 0, false
}

func skipSpaceAndComments(source string, start, limit int) int {
	i := start
	for i < limit {
		if unicode.IsSpace(rune(source[i])) {
			i++
			continue
		}
		if source[i] == '/' && i+1 < limit && source[i+1] == '/' {
			i = skipLineComment(source, i+2, limit)
			continue
		}
		if source[i] == '/' && i+1 < limit && source[i+1] == '*' {
			i = skipBlockComment(source, i+2, limit)
			continue
		}
		break
	}
	return i
}

func skipQuoted(source string, start, limit int) int {
	quote := source[start]
	for i := start + 1; i < limit; i++ {
		if source[i] == '\\' {
			i++
			continue
		}
		if source[i] == quote {
			return i + 1
		}
		if quote != '`' && (source[i] == '\n' || source[i] == '\r') {
			return i
		}
	}
	return limit
}

func skipLineComment(source string, start, limit int) int {
	for start < limit && source[start] != '\n' {
		start++
	}
	return start
}

func skipBlockComment(source string, start, limit int) int {
	for start+1 < limit {
		if source[start] == '*' && source[start+1] == '/' {
			return start + 2
		}
		start++
	}
	return limit
}

func isIdentifierStart(value byte) bool {
	return value == '_' || value == '$' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func isIdentifierPart(value byte) bool {
	return isIdentifierStart(value) || value >= '0' && value <= '9'
}

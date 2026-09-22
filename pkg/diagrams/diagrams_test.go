package diagrams

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnalyzeFilesReportsStaticDiagramProblems(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "diagram.tsx")
	source := `export const View = () => (
  <Diagram>
    {(id) => (<>
      <BoxNode id={id('source')} />
      <BoxNode id={id('source')} />
      <BoxNode id={id('target')} />
      <Arrow from={id('source')} to={id('missing')} />
      <Arrow from={id('unknown')} to={id('target')} />
      <Arrow from={id('source')} to={id('target')} />
      <Arrow from={id('source')} to={id('target')} />
    </>)}
  </Diagram>
)`
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))

	report, err := AnalyzeFiles([]string{path})
	require.NoError(t, err)
	require.Equal(t, SchemaVersion, report.SchemaVersion)
	require.Equal(t, 1, report.FilesAnalyzed)
	require.Equal(t, 1, report.DiagramsAnalyzed)
	require.Equal(t, []Diagnostic{
		{File: path, Line: 5, Column: 20, Severity: "error", Code: CodeDuplicateBoxID, Message: `duplicate BoxNode id "source" in this Diagram`},
		{File: path, Line: 7, Column: 38, Severity: "error", Code: CodeMissingTo, Message: `Arrow to endpoint "missing" has no matching BoxNode id in this Diagram`},
		{File: path, Line: 8, Column: 20, Severity: "error", Code: CodeMissingFrom, Message: `Arrow from endpoint "unknown" has no matching BoxNode id in this Diagram`},
		{File: path, Line: 10, Column: 20, Severity: "warning", Code: CodeDuplicateArrow, Message: `duplicate Arrow from "source" to "target" in this Diagram`},
	}, report.Diagnostics)
}

func TestAnalyzeFilesRecognizesFacetArrowFromToAndDuplicateEdges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "canonical.tsx")
	source := `<Diagram>{(id) => <>
  <BoxNode id={id('source')} />
  <BoxNode id={id('target')} />
  <Arrow from={id('source')} to={id('target')} />
  <Arrow from={id('source')} to={id('target')} />
  <Arrow from={id('missing')} to={id('unknown')} />
</>}</Diagram>`
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))

	report, err := AnalyzeFiles([]string{path})
	require.NoError(t, err)
	require.Equal(t, 1, report.DiagramsAnalyzed)
	require.Len(t, report.Diagnostics, 3)
	require.Equal(t, []string{CodeDuplicateArrow, CodeMissingFrom, CodeMissingTo}, []string{
		report.Diagnostics[0].Code, report.Diagnostics[1].Code, report.Diagnostics[2].Code,
	})
	require.Equal(t, `duplicate Arrow from "source" to "target" in this Diagram`, report.Diagnostics[0].Message)
	require.Equal(t, `Arrow from endpoint "missing" has no matching BoxNode id in this Diagram`, report.Diagnostics[1].Message)
	require.Equal(t, `Arrow to endpoint "unknown" has no matching BoxNode id in this Diagram`, report.Diagnostics[2].Message)
}

func TestAnalyzeFilesAcceptsCompatibilityArrowAliases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aliases.tsx")
	source := `<Diagram>{(id) => <>
  <BoxNode id={id('source')} />
  <BoxNode id={id('target')} />
  <Arrow start={id('source')} end={id('target')} />
</>}</Diagram>`
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))

	report, err := AnalyzeFiles([]string{path})
	require.NoError(t, err)
	require.Empty(t, report.Diagnostics)
}

func TestAnalyzeFilesCanonicalArrowPropsTakePrecedenceOverCompatibilityAliases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "precedence.tsx")
	source := `<Diagram>{(id) => <>
  <BoxNode id={id('source')} />
  <BoxNode id={id('target')} />
  <Arrow from={id('source')} start={id('wrong-from')} to={id('target')} end={id('wrong-to')} />
</>}</Diagram>`
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))

	report, err := AnalyzeFiles([]string{path})
	require.NoError(t, err)
	require.Empty(t, report.Diagnostics)
}

func TestAnalyzeFilesCanonicalPresenceSuppressesAliasesForUnsupportedExpressions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unsupported-canonical.tsx")
	source := `<Diagram>{(id) => <>
  <BoxNode id={id('source')} />
  <BoxNode id={id('target')} />
  <Arrow from="quoted-from" start={id('missing-from-quoted')} to="quoted-to" end={id('missing-to-quoted')} />
  <Arrow from={someValue} start={id('missing-from-expression')} to={makeEndpoint()} end={id('missing-to-expression')} />
</>}</Diagram>`
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))

	report, err := AnalyzeFiles([]string{path})
	require.NoError(t, err)
	require.Empty(t, report.Diagnostics)
}

func TestAnalyzeFilesKeepsDiagramScopesSeparateAndIgnoresDynamicCalls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scopes.tsx")
	source := `<>
  <Diagram>{(left) => <>
    <BoxNode id={left('same')} />
    <Arrow from={left(dynamic)} to={left('same')} />
  </>}</Diagram>
  <Diagram>{right => <>
    <BoxNode id={right('same')} />
    <Arrow from={right('same')} to={right(makeName())} />
  </>}</Diagram>
</>`
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))

	report, err := AnalyzeFiles([]string{path})
	require.NoError(t, err)
	require.Empty(t, report.Diagnostics)
	require.Equal(t, 1, report.FilesAnalyzed)
	require.Equal(t, 2, report.DiagramsAnalyzed)
}

func TestAnalyzeFilesAnalyzesMDXButNotFencesAndAcceptsMarkdownAsNoOp(t *testing.T) {
	dir := t.TempDir()
	mdx := filepath.Join(dir, "page.mdx")
	markdown := filepath.Join(dir, "page.md")
	require.NoError(t, os.WriteFile(mdx, []byte(`
~~~tsx
<Diagram>{(id) => <><Arrow from={id('x')} to={id('y')} /></>}</Diagram>
~~~

<!-- <Diagram>{(id) => <Arrow from={id('x')} to={id('y')} />}</Diagram> -->
<Diagram>{(id) => <>
  <BoxNode id={id('x')} />
  <Arrow from={id('x')} to={id('y')} />
</>}</Diagram>
`), 0o600))
	require.NoError(t, os.WriteFile(markdown, []byte(`<Diagram>{(id) => <Arrow from={id('x')} to={id('y')} />}</Diagram>`), 0o600))

	report, err := AnalyzeFiles([]string{mdx, markdown})
	require.NoError(t, err)
	require.Equal(t, 2, report.FilesAnalyzed)
	require.Equal(t, 1, report.DiagramsAnalyzed)
	require.Len(t, report.Diagnostics, 1)
	require.Equal(t, mdx, report.Diagnostics[0].File)
	require.Equal(t, CodeMissingTo, report.Diagnostics[0].Code)
	require.Equal(t, 9, report.Diagnostics[0].Line)
}

func TestAnalyzeFilesSkipsRegexLiteralsButKeepsJSX(t *testing.T) {
	path := filepath.Join(t.TempDir(), "regex.tsx")
	source := `const simple = /<Diagram>/;
const escapedSlash = /<Diagram>\/literal/;
const characterClass = /[/<Diagram>]/;
const flagged = /<Diagram>/giu;
const quotient = value / 2 / 3;
<div />; /<Diagram>/;
const jsxQuotient = <div /> / divisor; <Diagram>{(id) => <><BoxNode id={id('source')} /><BoxNode id={id('target')} /><Arrow from={id('source')} to={id('target')} /></>}</Diagram>`
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))

	report, err := AnalyzeFiles([]string{path})
	require.NoError(t, err)
	require.Equal(t, 1, report.DiagramsAnalyzed)
	require.Empty(t, report.Diagnostics)
}

func TestAnalyzeFilesRequiresWhitespaceAfterMarkdownClosingFence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fences.mdx")
	source := "```tsx\n" +
		"<Diagram>{(id) => <Arrow from={id('x')} to={id('y')} />}</Diagram>\n" +
		"```tsx\n" +
		"<Diagram>{(id) => <Arrow from={id('x')} to={id('y')} />}</Diagram>\n" +
		"```   \n" +
		"~~~~tsx\n" +
		"<Diagram>{(id) => <Arrow from={id('x')} to={id('y')} />}</Diagram>\n" +
		"~~~\n" +
		"<Diagram>{(id) => <Arrow from={id('x')} to={id('y')} />}</Diagram>\n" +
		"~~~~\t\n" +
		"<Diagram>{(id) => <><BoxNode id={id('x')} /><BoxNode id={id('y')} /><Arrow from={id('x')} to={id('y')} /></>}</Diagram>\n"
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))

	report, err := AnalyzeFiles([]string{path})
	require.NoError(t, err)
	require.Equal(t, 1, report.DiagramsAnalyzed)
	require.Empty(t, report.Diagnostics)
}

func TestAnalyzeFilesUsesOneBasedUnicodeColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unicode.tsx")
	require.NoError(t, os.WriteFile(path, []byte(`<Diagram>{(id) => <>é<Arrow from={id('x')} to={id('y')} /></>}</Diagram>`), 0o600))

	report, err := AnalyzeFiles([]string{path})
	require.NoError(t, err)
	require.Equal(t, 1, report.FilesAnalyzed)
	require.Equal(t, 1, report.DiagramsAnalyzed)
	require.Len(t, report.Diagnostics, 2)
	require.Equal(t, 35, report.Diagnostics[0].Column)
}

func TestAnalyzeFilesValidatesExplicitInputs(t *testing.T) {
	dir := t.TempDir()
	unsupported := filepath.Join(dir, "diagram.txt")
	require.NoError(t, os.WriteFile(unsupported, nil, 0o600))
	nonRegular := filepath.Join(dir, "directory.tsx")
	require.NoError(t, os.Mkdir(nonRegular, 0o700))

	_, err := AnalyzeFiles(nil)
	require.ErrorContains(t, err, "at least one")
	_, err = AnalyzeFiles([]string{unsupported})
	require.ErrorContains(t, err, "unsupported")
	_, err = AnalyzeFiles([]string{filepath.Join(dir, "missing.tsx")})
	require.ErrorContains(t, err, "missing.tsx")
	_, err = AnalyzeFiles([]string{nonRegular})
	require.ErrorContains(t, err, "not a regular file")
}

func TestAnalyzeFilesRejectsUnbalancedDiagramTags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.tsx")
	require.NoError(t, os.WriteFile(path, []byte(`<Diagram>{(id) => <BoxNode id={id('x')} />}`), 0o600))

	_, err := AnalyzeFiles([]string{path})
	require.ErrorContains(t, err, "opening Diagram")
}

package history

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The permission layer ignores a permissions.tools name the runtime does not
// declare, so a tool history can see in a transcript but api's tool table does
// not declare is a tool whose deny is silently dropped. This spec reads the
// names history's renderers and normalisers branch on straight from its source,
// so a new case cannot land without a matching declaration.

// claudeToolSwitchTags are the switch tags that branch on a Claude tool name.
// codex rows are normalised into Claude names before they reach these, which is
// why normalisedToolNames exist below.
var claudeToolSwitchTags = []string{"t.Tool", "tool", "toolUse.Tool", "tu.Tool"}

// codexToolNameExpr is the raw codex tool name, before normalisation.
const codexToolNameExpr = "call.Name"

// normalisedToolNames are names history synthesises while normalising codex
// rows; no agent ever calls a tool by them.
var normalisedToolNames = []string{"ApplyPatch", "CodexExecScript"}

func historyToolNames() (claude, codex []string) {
	GinkgoHelper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	Expect(err).NotTo(HaveOccurred())
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		Expect(err).NotTo(HaveOccurred())
		c, x := toolNamesIn(file)
		claude, codex = append(claude, c...), append(codex, x...)
	}
	for _, tool := range AllTools() {
		if !strings.HasPrefix(tool.ToolName(), "mcp__") {
			claude = append(claude, tool.ToolName())
		}
	}
	codex = append(codex, codexExecCommandTool)
	claude = slices.DeleteFunc(claude, func(name string) bool { return slices.Contains(normalisedToolNames, name) })
	codex = slices.DeleteFunc(codex, func(name string) bool { return name == "" })
	slices.Sort(claude)
	slices.Sort(codex)
	return slices.Compact(claude), slices.Compact(codex)
}

// toolNamesIn collects the string cases of tool-name switches, and the literals
// the raw codex name is compared against.
func toolNamesIn(file *ast.File) (claude, codex []string) {
	GinkgoHelper()
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.SwitchStmt:
			if n.Tag == nil {
				return true
			}
			tag := types.ExprString(n.Tag)
			for _, clause := range n.Body.List {
				names := stringLiterals(clause.(*ast.CaseClause).List)
				if slices.Contains(claudeToolSwitchTags, tag) {
					claude = append(claude, names...)
				} else if tag == codexToolNameExpr {
					codex = append(codex, names...)
				}
			}
		case *ast.BinaryExpr:
			if (n.Op == token.EQL || n.Op == token.NEQ) && types.ExprString(n.X) == codexToolNameExpr {
				codex = append(codex, stringLiterals([]ast.Expr{n.Y})...)
			}
		}
		return true
	})
	return claude, codex
}

func stringLiterals(exprs []ast.Expr) []string {
	var out []string
	for _, expr := range exprs {
		lit, ok := expr.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		value, err := strconv.Unquote(lit.Value)
		Expect(err).NotTo(HaveOccurred())
		out = append(out, value)
	}
	return out
}

func declaredToolNames(provider *api.ModelProvider) []string {
	var out []string
	for _, tool := range api.AgentToolsFor(provider, api.ModeCLI) {
		out = append(out, tool.Name)
	}
	return out
}

var _ = Describe("agent tool vocabulary drift", func() {
	It("declares every Claude and codex tool name history recognises", func() {
		claude, codex := historyToolNames()
		Expect(claude).To(ContainElements("Bash", "AskUserQuestion", "BashOutput"), "the source scan found the claude switches")
		Expect(codex).To(ContainElements("apply_patch", "exec", "exec_command"), "the source scan found the codex switches")

		Expect(declaredToolNames(api.Anthropic)).To(ContainElements(claude))
		Expect(declaredToolNames(api.OpenAI)).To(ContainElements(codex))
	})
})

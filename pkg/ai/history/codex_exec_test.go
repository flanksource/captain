package history

import (
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The freeform `exec` tool had no test at all, which is the direct reason it ran
// at a 0.17% parse rate for the life of the feature: 39 306 scripts fell through
// to a raw JavaScript row and not one recorded an error. These cases are the
// script shapes the corpus actually contains, so a traversal that stops reaching
// one of them fails here rather than silently downgrading the transcript.

// execScriptRows normalizes a freeform exec call the way the parser path does.
func execScriptRows(script string) []ToolUse {
	return NormalizeCodexToolCalls(CodexToolCall{
		Name:       "exec",
		Input:      map[string]any{"input": script},
		CWD:        "/repo",
		ID:         "call-exec",
		SessionID:  "session-1",
		RecordType: "response_item.custom_tool_call",
	})
}

// rowCommands returns the rendered shell command of every Bash row.
func rowCommands(rows []ToolUse) []string {
	var out []string
	for _, row := range rows {
		if row.Tool != "Bash" {
			continue
		}
		out = append(out, stringValue(row.Input["command"]))
	}
	return out
}

var _ = Describe("freeform exec scripts", func() {
	// Every shape below appears verbatim in the rollout corpus. The command text
	// is what the row must render; anything less means the traversal reached the
	// call but could not resolve its arguments.
	DescribeTable("resolve to the commands the script runs",
		func(script, want string) {
			rows := execScriptRows(script)

			Expect(rowCommands(rows)).To(Equal([]string{want}),
				"script did not resolve to a rendered command:\n%s", script)
			for _, row := range rows {
				Expect(row.Input).ToNot(HaveKey("parse_error"))
			}
		},

		Entry("a plain awaited call",
			`const r = await tools.exec_command({cmd: "git status --short", workdir: "/repo"});
text(r.output);`,
			"git status --short"),

		Entry("a command bound to a script-scope const",
			`const cmd = "go test ./pkg/session/";
const r = await tools.exec_command({cmd, workdir: "/repo"});
text(r.output);`,
			"go test ./pkg/session/"),

		Entry("a template-literal workdir with no interpolation",
			"const r = await tools.exec_command({cmd: \"ls -la\", workdir: `/repo/pkg`});\ntext(r.output);",
			"ls -la"),

		// The dominant multi-command shape: a literal table pushed through
		// Promise.all with the command destructured out of each element. Before
		// the unroll this rendered as a single `bash {{js:cmd}}` row.
		Entry("a destructured literal table under Promise.all",
			`const jobs = [["status", "git status --short"], ["log", "git log -1 --oneline"]];
const results = await Promise.all(jobs.map(async ([name, cmd]) => {
  const r = await tools.exec_command({cmd, workdir: "/repo"});
  return {name, output: r.output};
}));
text(JSON.stringify(results));`,
			"git status --short\ngit log -1 --oneline"),

		Entry("a for-of loop over a literal array",
			`for (const cmd of ["make lint", "make build"]) {
  const r = await tools.exec_command({cmd, workdir: "/repo"});
  text(r.output);
}`,
			"make lint\nmake build"),

		Entry("a forEach callback over object elements",
			`const steps = [{cmd: "pwd"}, {cmd: "whoami"}];
steps.forEach((step) => { tools.exec_command({cmd: step.cmd, workdir: "/repo"}); });`,
			"pwd\nwhoami"),
	)

	// The placeholder is why this matters: a row reading `bash {{js:cmd}}` claims
	// a command was run and names something that is not one. An honest failure is
	// the script itself.
	It("never renders a mustache placeholder, whatever the script does", func() {
		scripts := []string{
			`const r = await tools.exec_command({cmd: process.argv.slice(2).join(" "), workdir: "/repo"});`,
			`const dir = someUnknownHelper();
await tools.exec_command({cmd: "ls", workdir: ` + "`${dir}/pkg`" + `});`,
			`const cmds = buildCommands();
await tools.exec_command({cmd: cmds.map((x) => JSON.stringify(x)).join(" ")});`,
		}

		for _, script := range scripts {
			for _, row := range execScriptRows(script) {
				for key, value := range row.Input {
					Expect(fmt.Sprint(value)).ToNot(ContainSubstring("{{js:"),
						"input[%q] leaked a placeholder for script:\n%s", key, script)
				}
				// An unresolvable argument is a parse failure, and the row shows the
				// script rather than a command it cannot name.
				Expect(row.Tool).To(Equal("CodexExecScript"))
				Expect(row.Input).To(HaveKey("parse_error"))
			}
		}
	})

	// The counterpart: a call site the traversal never reached used to return an
	// empty command with no error, which is indistinguishable from a script that
	// simply prints. Only real call sites count, so a mention inside a string or
	// a comment must not manufacture a failure.
	It("reports a parse failure only when a real call site resolved to nothing", func() {
		mentionOnly := execScriptRows(
			"const doc = \"tools.exec_command({cmd: 'never run'})\";\n" +
				"// tools.exec_command({cmd: \"also never run\"})\n" +
				"text(doc);")

		Expect(mentionOnly).To(HaveLen(1))
		Expect(mentionOnly[0].Tool).To(Equal("CodexExecScript"))
		Expect(mentionOnly[0].Input).ToNot(HaveKey("parse_error"))
	})

	// exec_command is not the only tool a script drives: apply_patch, write_stdin
	// and update_plan account for 8 271 further invocations, and each has a
	// first-class row when Codex sends it as a function_call. The freeform form
	// must produce the same rows rather than a second, worse-rendered path.
	It("gives every non-shell tool in a script its own first-class row", func() {
		script := `await tools.exec_command({cmd: "go build ./...", workdir: "/repo"});
await tools.apply_patch({input: "*** Begin Patch\n*** Add File: pkg/new.go\n+package pkg\n*** End Patch"});
await tools.update_plan({plan: [{step: "build", status: "completed"}]});`

		rows := execScriptRows(script)

		var tools []string
		for _, row := range rows {
			tools = append(tools, row.Tool)
		}
		Expect(tools).To(Equal([]string{"Bash", "Write", "TodoWrite"}))

		// One call, several operations: the rows share the call's id so they stay
		// attributable to it, distinguished by an index suffix.
		Expect(rows[0].ToolUseID).To(Equal("call-exec"))
		Expect(rows[1].ToolUseID).To(Equal("call-exec#1"))
		Expect(rows[2].ToolUseID).To(Equal("call-exec#2"))

		Expect(rows[1].Input["file_path"]).To(Equal("pkg/new.go"))
		Expect(strings.TrimSpace(stringValue(rows[1].Input["content"]))).To(Equal("package pkg"))
	})

	// A script that only prints is not a defect. It ran, so it earns a row, and
	// that row shows the script because there is no operation to name.
	It("keeps a script with no tool call as a script row", func() {
		rows := execScriptRows(`text("nothing to do here");`)

		Expect(rows).To(HaveLen(1))
		Expect(rows[0].Tool).To(Equal("CodexExecScript"))
		Expect(rows[0].Input["script"]).To(Equal(`text("nothing to do here");`))
		Expect(rows[0].Input).ToNot(HaveKey("parse_error"))
	})
})

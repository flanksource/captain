package cli

type promptHelpField struct {
	Path    string
	Meaning string
}

const promptHelpExample = `---
name: Release notes
description: Summarize a change as structured release notes
model: agent:sonnet
effort: high
fallbacks:
  - api:gemini-3.5-flash:high
config:
  maxOutputTokens: 2000
  temperature: 0.2
input:
  schema:
    type: object
    required: [change]
    properties:
      change: {type: string}
  default:
    audience: operators
output:
  schema:
    type: object
    required: [summary]
    properties:
      summary: {type: string}
runtimes:
  - agent:sonnet:high
  - model: gemini-3.5-flash
    mode: api
    effort: high
budget:
  cost: 0.50
  maxTokens: 2000
  maxTurns: 4
  timeout: 20m
permissions:
  mode: acceptEdits
  tools:
    Read: allow
    Edit: ask
memory:
  skipUser: true
setup:
  cwd: .
sandbox:
  backend: srt
  policy:
    paths: ["pkg/**", "!secrets/**"]
    maxAttempts: 2
---
{{role "system"}}
Write concise release notes for {{audience}}.
{{role "user"}}
Summarize this change:

{{change}}`

func promptDotpromptHelpFields() []promptHelpField {
	return []promptHelpField{
		{"name, description", "Human-readable prompt catalog metadata."},
		{"model", "Default model selector. Compact selectors may include runtime mode and effort, for example agent:sonnet:high."},
		{"config.maxOutputTokens", "Maximum output tokens for each model call; takes precedence over budget.maxTokens when both are present."},
		{"config.temperature", "Sampling temperature; takes precedence over top-level temperature."},
		{"config.reasoning", "Reasoning-effort string; takes precedence over the top-level effort field."},
		{"input.schema", "Input Picoschema or JSON Schema used by dotprompt to describe and validate template variables."},
		{"input.default", "Default values merged into missing template variables."},
		{"output.schema", "Output Picoschema or raw JSON Schema sent to the model as the structured-output contract."},
		{"runtimes[]", "Two or more default parallel targets. Each entry is a compact model selector or a full model/mode/effort object."},
	}
}

func promptSpecHelpFields() []promptHelpField {
	return []promptHelpField{
		{"model, id", "Catalog model name and optional fully qualified provider ID. The provider is derived from the name; the mechanism is the separate mode field below."},
		{"mode", "Runtime mechanism: api, cli, agent, or cmux."},
		{"temperature", "Sampling temperature from 0 through 2. config.temperature wins when both are set."},
		{"effort", "Reasoning effort: low, medium, high, xhigh, max, or ultra."},
		{"noCache", "Disable Captain's response cache for this run."},
		{"fallbacks[]", "Ordered alternative models, each as a compact selector or full model object. Nested fallback lists are ignored."},
		{"streaming, mediaTypes, resume, interrupt, steer, callerTools", "Resolved runtime capabilities. These are read-only output fields and must not be authored to claim unsupported behavior."},
		{"prompt.user, prompt.system", "Single-turn user and system text. Role-marked Handlebars body text overrides these values."},
		{"prompt.appendSystem", "Text appended to the runtime's default system prompt."},
		{"prompt.source", "Diagnostic source label; Captain normally fills this from the .prompt path."},
		{"prompt.schemaJSON", "Raw JSON Schema in the native run spec. In .prompt files, prefer output.schema."},
		{"prompt.schemaStrictness", "Schema failure policy: runtime default, none, warning, error, or retry."},
		{"prompt.metadata", "Arbitrary string-to-string diagnostic metadata."},
		{"prompt.attachments[]", "Ordered multimodal inputs. Each has exactly one source (id, path, or url), plus optional filename, mediaType, size, and sha256 metadata."},
		{"messages[].role", "Role for one provider-neutral history entry: system, user, assistant, or tool."},
		{"messages[].parts[]", "Each part has type text, reasoning, attachment, tool-request, or tool-result and exactly one matching payload."},
		{"messages[].parts[].toolRequest", "Tool request with name, toolCallId, and JSON input; tool results must correlate with these stable call IDs."},
		{"messages[].parts[].toolResult", "Tool result with toolCallId and either JSON output or an error. messages[] is mutually exclusive with prompt body fields and toolApproval."},
		{"budget.cost", "Maximum spend in USD; zero means no ceiling."},
		{"budget.maxTokens", "Maximum output tokens per call; zero uses the runtime default. config.maxOutputTokens wins when set."},
		{"budget.maxTurns", "Maximum agent turns from 0 through 100; zero uses the runtime default."},
		{"budget.timeout", "Overall run duration such as 30m; empty uses the caller default."},
		{"memory.skills[]", "Additional skill or plugin directories to load."},
		{"memory.skipProject, memory.skipUser", "Skip project-local or user-level ambient settings."},
		{"memory.skipSkills, memory.skipHooks, memory.skipMemory", "Disable skills, hooks, or auto-memory/agent instruction files."},
		{"memory.bare", "Skip hooks, skills, memory, and ambient settings together."},
		{"permissions.mode", "Permission posture, independent of the sandbox: default, plan, acceptEdits, auto, bypassPermissions, or dontAsk."},
		{"permissions.presets[]", "Named safety bundles: edit or bare."},
		{"permissions.tools.<tool>", "Per-tool policy: auto, ask, allow, or deny. Captain fails if the selected runtime cannot enforce it."},
		{"permissions.mcp.disabled", "Disable all MCP servers."},
		{"permissions.mcp.servers[]", "Optional allowlist of configured MCP servers."},
		{"permissions.mcp.<server>", "Enable or disable one configured MCP server."},
		{"permissions.plugins.<path>, permissions.skills.<path>", "Enable or disable a plugin or skill directory."},
		{"toolPreferences.<tool-or-group>", "Per-turn exposure policy for a tool name or group: auto, ask, allow, or deny."},
		{"toolApproval.state.messages", "Complete provider-neutral conversation ending with the suspended assistant tool requests."},
		{"toolApproval.state.calls[]", "Recorded calls: request.toolCallId, request.tool, optional JSON request.input, and an optional completed result."},
		{"toolApproval.decisions[]", "One decision per pending call: approvalId, toolCallId, tool, action (approve, deny, or respond), and action-specific input, message, or result."},
		{"toolApproval", "Advanced durable resume mode; it is mutually exclusive with prompt text and messages. Use --schema for the exact nested result shapes."},
		{"setup.cwd, setup.baseDir", "Working directory and base directory used while preparing the run."},
		{"setup.dotenv[], setup.envVars[]", "Dotenv files plus environment entries with name and either value or valueFrom, resolved at the runtime boundary."},
		{"setup.connections", "External connection material projected into the prepared environment. Use --schema for the provider-owned nested forms."},
		{"setup.checkout.mode", "Checkout mode: none, local, or remote."},
		{"setup.checkout.url, setup.checkout.path, setup.checkout.connection", "Remote URL, local source path, or named connection used for checkout."},
		{"setup.checkout.ref, setup.checkout.depth, setup.checkout.since", "Git ref, clone depth, and optional commit-ish used for dirty-file reporting."},
		{"setup.checkout.dirty", "Deprecated no-op retained only for decoding existing files; do not use in new prompt files."},
		{"setup.checkout.worktree.mode", "Worktree mode: none, new, or existing."},
		{"setup.checkout.worktree.prefix, setup.checkout.worktree.base, setup.checkout.worktree.path", "Branch prefix, base ref, and explicit worktree path."},
		{"setup.checkout.worktree.keep", "Keep the worktree after the run."},
		{"setup.checkout.worktree.uncommitted", "clone or skip staged, unstaged, and untracked source changes without mutating the source tree."},
		{"setup.checkout.worktree.ignored", "clone or skip gitignored content such as dependency and build directories."},
		{"sandbox", "Scalar backend name, or an object. Bare adapters are none, srt, container, and git-agent."},
		{"sandbox.backend, sandbox.agent", "Configured sandbox/bare adapter and optional pinned git-agent worker."},
		{"sandbox.policy.paths[]", "Gitignore-style allow/deny paths; ! negates a pattern."},
		{"sandbox.policy.maxAttempts", "Maximum submit attempts; zero inherits the configured sandbox policy."},
		{"workflow.verify.commands[]", "Shell commands whose exit status votes on the generated result."},
		{"workflow.verify.fixture", "Gavel fixture markdown carried in the spec; Gavel, not Captain, executes it."},
		{"workflow.verify.prompts[]", "LLM-judge .prompt paths; each must return ok, reason, and feedback."},
		{"workflow.verify.scope", "Verify all files or only changed files."},
		{"workflow.verify.maxIterations", "Maximum generate/verify iterations; zero uses the run default of one."},
		{"workflow.commits[].on", "Commit phase: turn, agent, or run."},
		{"workflow.commits[].mode", "Commit shape: commit, fixup, or amend."},
		{"workflow.commits[].when", "Outcome gate: always, onSuccess, or onVerify."},
		{"workflow.commits[].message, .anchor, .squash, .base", "Commit subject and fixup/autosquash controls."},
		{"workflow.commits[].stage", "Stage the isolated worktree or only Captain-recorded changed files."},
		{"workflow.commits[].gates", "Pre-commit checks: none, cheap, or full."},
		{"workflow.commits[].dryRun", "Report the proposed commit without writing it."},
		{"workflow.autoVerifyWithoutFixture", "Allow a successful generate-only run to become durably verified without a fixture."},
		{"sessionId", "Resume a provider session by ID when the selected runtime supports resume."},
		{"cliArgs", "Provider-specific cmux CLI arguments keyed by their JSON field names; ignored outside cmux mode. Use --schema for the selected runtime's exact fields."},
	}
}

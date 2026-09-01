// agent.ts is captain's claude-agent backend: a long-lived JSON-RPC 2.0 stdio
// server wrapping the Claude Agent SDK. It is go:embed'd into the Go provider
// (pkg/ai/provider/claudeagent) and run via tsx as a clicky-supervised process.
//
// Protocol (newline-delimited JSON, one object per line on stdout):
//   client -> server requests:
//     initialize {cwd, model, systemPrompt, appendSystemPrompt, allowedTools,
//                 maxTurns, maxBudgetUsd, permissionMode, resume, approvalMode,
//                 outputSchema, mcpServers}
//                 -> reply {ok:true}
//     prompt {text, attachments?} -> reply {accepted:true}
//     interrupt          -> reply {}
//     shutdown           -> reply {} then exit
//   server -> client notifications:
//     session/init   {session_id, model, tools}
//     message/text   {text}
//     message/thinking {text}
//     message/tool_use {tool, input, id}
//     message/tool_result {id, content, is_error}
//     turn/completed {success, subtype, session_id, cost_usd, usage, num_turns,
//                     result_text, structured_output}
//     turn/error     {message}
//   server -> client requests (only when approvalMode === "ask"):
//     can_use_tool {tool, input, tool_use_id}
//                 -> reply {allow, message?, updatedInput?}
//   The transport is bidirectional: the host's reply to can_use_tool arrives on
//   stdin as an id-bearing response (no method) and resolves the pending callHost
//   promise, so a tool call blocks only until the host decides.
//
// One query({prompt, options}) SDK session stays alive for the whole process;
// turns are fed by pushing user messages onto TurnQueue (a push async-iterable),
// which keeps the session (and any resumed history) alive across turns.

import { query } from "@anthropic-ai/claude-agent-sdk";
import type {
  Options,
  PreToolUseHookInput,
  Query,
  SDKMessage,
} from "@anthropic-ai/claude-agent-sdk";
import { createInterface } from "readline";
import {
  callHost,
  diag,
  handleResponse,
  type JsonRpcId,
  notify,
  type PromptParams,
  reply,
  replyError,
  TurnQueue,
} from "./protocol.js";

// Strip nested-session markers so the SDK does not refuse to run inside captain
// (which may itself have been launched from a Claude Code session). The Go
// runner also blanks these, but deleting here is the authoritative strip since
// the SDK reads process.env at query() time.
delete process.env.CLAUDECODE;
delete process.env.CLAUDE_CODE_ENTRYPOINT;

interface InitializeParams {
  cwd?: string;
  model?: string;
  systemPrompt?: string;
  appendSystemPrompt?: string;
  allowedTools?: string[];
  disallowedTools?: string[];
  maxTurns?: number;
  maxBudgetUsd?: number;
  permissionMode?: string;
  sandbox?: Options["sandbox"];
  resume?: string;
  approvalMode?: string;
  // outputSchema is the JSON Schema captain derives from the request's
  // structured-output target. Present => the SDK is asked for validated JSON
  // (options.outputFormat) and every turn's result carries structured_output.
  outputSchema?: Record<string, unknown>;
  // monitorUrl is the captain serve base URL session-monitoring lifecycle
  // hooks POST to. Empty/absent disables monitoring hook injection.
  monitorUrl?: string;
  mcpServers?: Record<
    string,
    { type: "http"; url: string; headers?: Record<string, string> }
  >;
  callerToolUseIDKey?: string;
}

// HostDecision is the can_use_tool reply shape from the Go host.
interface HostDecision {
  allow?: boolean;
  message?: string;
  updatedInput?: Record<string, unknown>;
}

let turns: TurnQueue | null = null;
let activeQuery: Query | null = null;
let callerToolServers: string[] = [];

function callerToolName(toolName: string): string | undefined {
  for (const server of callerToolServers) {
    const prefix = `mcp__${server}__`;
    if (toolName.startsWith(prefix) && toolName.length > prefix.length) {
      return toolName.slice(prefix.length);
    }
  }
  return undefined;
}

function buildOptions(params: InitializeParams): Options {
  // brokered: the host vets each tool over the can_use_tool round-trip, so the
  // SDK must consult canUseTool rather than auto-approving. bypassPermissions /
  // allowDangerouslySkipPermissions would skip canUseTool entirely.
  const brokered = params.approvalMode === "ask";
  // The host always sends a resolved mode; "default" is the floor when it did
  // not. Skipping permissions is gated on the caller having ASKED for bypass —
  // keying it on "no broker attached" turned every unbrokered run, which is
  // almost all of them, into an unconfined one.
  const permissionMode = (params.permissionMode as Options["permissionMode"]) ||
    "default";
  const options: Options = {
    cwd: params.cwd,
    model: params.model,
    maxTurns: params.maxTurns || undefined,
    maxBudgetUsd: params.maxBudgetUsd || undefined,
    permissionMode,
    sandbox: params.sandbox,
    allowDangerouslySkipPermissions:
      !brokered && permissionMode === "bypassPermissions",
    allowedTools:
      params.allowedTools && params.allowedTools.length
        ? params.allowedTools
        : undefined,
    disallowedTools:
      params.disallowedTools && params.disallowedTools.length
        ? params.disallowedTools
        : undefined,
    mcpServers: params.mcpServers,
    stderr: (data: string) => process.stderr.write(data),
    hooks: {
      PreToolUse: [
        {
          matcher: "Bash",
          hooks: [
            async (input) => {
              const cmd = (input as { tool_input?: { command?: string } })
                .tool_input?.command || "";
              if (/\bgit\s+(add|commit)\b/.test(cmd)) {
                return {
                  decision: "block" as const,
                  reason: "git add/commit is managed by captain, not the agent",
                };
              }
              return { decision: "approve" as const };
            },
          ],
        },
      ],
    },
  };

  if (callerToolServers.length > 0) {
    const callerToolUseIDKey = params.callerToolUseIDKey;
    if (!callerToolUseIDKey) {
      throw new Error("caller tools require a provider tool-use ID key");
    }
    options.hooks?.PreToolUse?.push({
      hooks: [
        async (input, toolUseID) => {
          const hook = input as PreToolUseHookInput;
          if (!callerToolName(hook.tool_name)) {
            return {};
          }
          if (!toolUseID && !hook.tool_use_id) {
            return {
              decision: "block" as const,
              reason: "caller tool has no Claude tool-use ID",
            };
          }
          if (
            typeof hook.tool_input !== "object" ||
            hook.tool_input === null ||
            Array.isArray(hook.tool_input)
          ) {
            return {
              decision: "block" as const,
              reason: "caller tool input must be an object",
            };
          }
          return {
            hookSpecificOutput: {
              hookEventName: "PreToolUse" as const,
              permissionDecision: "allow" as const,
              updatedInput: {
                ...(hook.tool_input as Record<string, unknown>),
                [callerToolUseIDKey]: toolUseID || hook.tool_use_id,
              },
            },
          };
        },
      ],
    });
  }

  // Session-monitoring lifecycle hooks: fire-and-forget POSTs to captain
  // serve so the session appears in the database in real time. A monitoring
  // failure (serve down, slow) must never block or slow the agent turn.
  if (params.monitorUrl) {
    const monitorUrl = params.monitorUrl.replace(/\/+$/, "");
    const monitorEvents = [
      "SessionStart",
      "UserPromptSubmit",
      "Stop",
      "SubagentStop",
      "SessionEnd",
    ] as const;
    const hooks = options.hooks as unknown as Record<string, unknown[]>;
    for (const event of monitorEvents) {
      hooks[event] = [
        {
          hooks: [
            async (input: Record<string, unknown>) => {
              fetch(`${monitorUrl}/api/captain/hooks/claude`, {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                  event,
                  sessionId: input.session_id,
                  transcriptPath: input.transcript_path,
                  cwd: input.cwd,
                  detail: input.source ?? input.reason ?? undefined,
                }),
                signal: AbortSignal.timeout(1000),
              }).catch(() => {});
              return {};
            },
          ],
        },
      ];
    }
  }

  // appendSystemPrompt is not a top-level Options field; it must ride on the
  // claude_code preset. A custom systemPrompt string replaces the default.
  if (params.systemPrompt && params.appendSystemPrompt) {
    options.systemPrompt = params.systemPrompt + "\n\n" + params.appendSystemPrompt;
  } else if (params.systemPrompt) {
    options.systemPrompt = params.systemPrompt;
  } else if (params.appendSystemPrompt) {
    options.systemPrompt = {
      type: "preset",
      preset: "claude_code",
      append: params.appendSystemPrompt,
    };
  }

  if (params.resume) {
    options.resume = params.resume;
  }

  // Structured output: ask the SDK to validate the final answer against the
  // caller's JSON Schema and return it on result.structured_output. The schema
  // is a session-level option, so it applies to every turn of this query().
  if (params.outputSchema) {
    options.outputFormat = {
      type: "json_schema",
      schema: params.outputSchema,
    };
  }

  // When the host brokers approvals, forward each tool-permission check to it
  // over can_use_tool and map the decision onto the SDK PermissionResult. The
  // PreToolUse git add/commit block above still applies first.
  if (brokered) {
    options.canUseTool = async (toolName, input, opts) => {
      if (
        Object.keys(params.mcpServers ?? {}).some((server) =>
          toolName.startsWith(`mcp__${server}__`),
        )
      ) {
        return { behavior: "allow", updatedInput: input };
      }
      let decision: HostDecision;
      try {
        decision = (await callHost("can_use_tool", {
          tool: toolName,
          input,
          tool_use_id: opts.toolUseID,
        })) as HostDecision;
      } catch (err) {
        return {
          behavior: "deny",
          message: `permission bridge error: ${(err as Error)?.message || err}`,
        };
      }
      if (decision?.allow) {
        return { behavior: "allow", updatedInput: decision.updatedInput ?? input };
      }
      return { behavior: "deny", message: decision?.message || "denied by host" };
    };
  }

  return options;
}

function handleInitialize(id: JsonRpcId, params: InitializeParams) {
  if (turns) {
    // Already initialized; treat as idempotent so a re-bind after a restart is
    // not fatal.
    reply(id, { ok: true });
    return;
  }
  try {
    callerToolServers = Object.keys(params.mcpServers ?? {});
    turns = new TurnQueue();
    activeQuery = query({ prompt: turns, options: buildOptions(params) });
    reply(id, { ok: true });
    pump(activeQuery).catch((err) => {
      notify("turn/error", { message: err?.message || String(err) });
    });
  } catch (err) {
    callerToolServers = [];
    turns = null;
    activeQuery = null;
    replyError(id, -32603, `initialize failed: ${(err as Error)?.message || err}`);
  }
}

function handlePrompt(id: JsonRpcId, params: PromptParams) {
  if (!turns) {
    replyError(id, -32002, "not initialized");
    return;
  }
  try {
    turns.push(params);
    reply(id, { accepted: true });
  } catch (err) {
    replyError(id, -32602, `invalid prompt: ${(err as Error)?.message || err}`);
  }
}

async function handleInterrupt(id: JsonRpcId) {
  try {
    if (activeQuery) {
      await activeQuery.interrupt();
    }
  } catch (err) {
    diag(`interrupt: ${(err as Error)?.message || err}`);
  }
  reply(id, {});
}

function handleShutdown(id: JsonRpcId) {
  if (turns) {
    turns.end();
  }
  reply(id, {});
  // Give stdout a tick to flush the reply before exiting.
  setTimeout(() => process.exit(0), 50);
}

async function pump(stream: Query) {
  for await (const message of stream) {
    handleMessage(message);
  }
}

// stringifyToolResult flattens an SDK tool_result `content` (a string, or an
// array of content blocks) into plain text for the message/tool_result payload.
function stringifyToolResult(content: unknown): string {
  if (typeof content === "string") {
    return content;
  }
  if (Array.isArray(content)) {
    return content
      .map((block) => {
        const rec = block as Record<string, unknown>;
        return typeof rec.text === "string" ? rec.text : JSON.stringify(block);
      })
      .join("");
  }
  if (content == null) {
    return "";
  }
  return JSON.stringify(content);
}

function handleMessage(message: SDKMessage) {
  switch (message.type) {
    case "system":
      if ((message as { subtype?: string }).subtype === "init") {
        notify("session/init", {
          session_id: message.session_id,
          model: (message as { model?: string }).model,
          tools: (message as { tools?: string[] }).tools,
        });
      }
      break;

    case "assistant": {
      const content =
        (message as { message?: { content?: unknown[] } }).message?.content ?? [];
      for (const block of content as Array<Record<string, unknown>>) {
        if (block.type === "text") {
          notify("message/text", { text: block.text });
        } else if (block.type === "thinking") {
          notify("message/thinking", { text: block.thinking });
        } else if (block.type === "tool_use") {
          notify("message/tool_use", {
            tool: callerToolName(String(block.name)) ?? block.name,
            input: block.input,
            id: block.id,
          });
        }
      }
      break;
    }

    case "user": {
      // Tool results arrive as tool_result blocks on user-role messages.
      const content =
        (message as { message?: { content?: unknown[] } }).message?.content ?? [];
      for (const block of content as Array<Record<string, unknown>>) {
        if (block.type === "tool_result") {
          notify("message/tool_result", {
            id: block.tool_use_id,
            content: stringifyToolResult(block.content),
            is_error: block.is_error === true,
          });
        }
      }
      break;
    }

    case "result":
      notify("turn/completed", {
        success: !(message as { is_error?: boolean }).is_error,
        subtype: (message as { subtype?: string }).subtype,
        session_id: message.session_id,
        cost_usd: (message as { total_cost_usd?: number }).total_cost_usd,
        usage: (message as { usage?: unknown }).usage,
        num_turns: (message as { num_turns?: number }).num_turns,
        result_text: (message as { result?: string }).result,
        structured_output: (message as { structured_output?: unknown })
          .structured_output,
      });
      break;
  }
}

const rl = createInterface({ input: process.stdin });
rl.on("line", (line) => {
  const trimmed = line.trim();
  if (!trimmed) {
    return;
  }
  let req: {
    id?: JsonRpcId;
    method?: string;
    params?: unknown;
    result?: unknown;
    error?: { code: number; message: string };
  };
  try {
    req = JSON.parse(trimmed);
  } catch {
    diag(`ignoring non-JSON line: ${trimmed.slice(0, 200)}`);
    return;
  }
  // A response to one of our callHost requests (id, no method) resolves it.
  if (req.method === undefined && handleResponse(req)) {
    return;
  }
  const id = req.id ?? null;
  switch (req.method) {
    case "initialize":
      handleInitialize(id, (req.params as InitializeParams) || {});
      break;
    case "prompt":
      handlePrompt(id, (req.params as PromptParams) || {});
      break;
    case "interrupt":
      handleInterrupt(id);
      break;
    case "shutdown":
      handleShutdown(id);
      break;
    default:
      if (req.id !== undefined) {
        replyError(id, -32601, `method not found: ${req.method}`);
      }
  }
});
rl.on("close", () => {
  if (turns) {
    turns.end();
  }
});

// messages.ts maps the SDK's message stream onto the bridge's server -> client
// notifications (see the protocol summary at the top of agent.ts).

import type { SDKMessage } from "@anthropic-ai/claude-agent-sdk";
import { notify } from "./protocol.js";

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

// streamedBlocks holds the text already sent as deltas for each content block
// of the API message now streaming, keyed by the stream's block index. The SDK
// repeats every block whole once it completes — as one assistant message per
// block, each carrying it at content position 0 — so a completed block is
// matched to a streamed one by its text, never by its position.
const streamedBlocks = new Map<number, string>();

function handleStreamEvent(message: SDKMessage) {
  const event = (message as { event?: Record<string, unknown> }).event;
  if (event?.type === "message_start") {
    streamedBlocks.clear();
    return;
  }
  if (!event || event.type !== "content_block_delta") {
    return;
  }
  const index = typeof event.index === "number" ? event.index : 0;
  const delta = event.delta as Record<string, unknown> | undefined;
  if (!delta) {
    return;
  }
  let text: string | undefined;
  if (delta.type === "text_delta" && typeof delta.text === "string" && delta.text) {
    text = delta.text;
    notify("message/text", { text });
  } else if (
    delta.type === "thinking_delta" &&
    typeof delta.thinking === "string" &&
    delta.thinking
  ) {
    text = delta.thinking;
    notify("message/thinking", { text });
  }
  if (text !== undefined) {
    streamedBlocks.set(index, (streamedBlocks.get(index) ?? "") + text);
  }
}

// consumeStreamed reports whether a completed block's text was already
// streamed, and forgets it so an identical later block is still forwarded.
function consumeStreamed(text: unknown): boolean {
  for (const [index, streamed] of streamedBlocks) {
    if (streamed === text) {
      streamedBlocks.delete(index);
      return true;
    }
  }
  return false;
}

// parentToolUseID is the Agent tool call that spawned the subagent this message
// came from, or null for the main thread's own messages.
function parentToolUseID(message: SDKMessage): string | null {
  return (message as { parent_tool_use_id?: string | null }).parent_tool_use_id ?? null;
}

// handleMessage notifies the host of one SDK message. toolName maps the SDK's
// tool name onto the one the host knows the tool by.
export function handleMessage(
  message: SDKMessage,
  toolName: (name: string) => string,
) {
  switch (message.type) {
    case "stream_event":
      // A subagent's narration is not the parent's answer; its result reaches
      // the parent as a tool result or task notification.
      if (parentToolUseID(message) === null) {
        handleStreamEvent(message);
      }
      break;

    case "system": {
      const subtype = (message as { subtype?: string }).subtype;
      if (subtype === "init") {
        notify("session/init", {
          session_id: message.session_id,
          model: (message as { model?: string }).model,
          tools: (message as { tools?: string[] }).tools,
        });
      } else if (subtype === "session_state_changed") {
        notify("session/state", { state: (message as { state?: string }).state });
      }
      break;
    }

    case "assistant": {
      const content =
        (message as { message?: { content?: unknown[] } }).message?.content ?? [];
      const parent = parentToolUseID(message);
      const blocks = content as Array<Record<string, unknown>>;
      for (const block of blocks) {
        if (block.type === "text") {
          if (parent === null && !consumeStreamed(block.text)) {
            notify("message/text", { text: block.text });
          }
        } else if (block.type === "thinking") {
          if (parent === null && !consumeStreamed(block.thinking)) {
            notify("message/thinking", { text: block.thinking });
          }
        } else if (block.type === "tool_use") {
          notify("message/tool_use", {
            tool: toolName(String(block.name)),
            input: block.input,
            id: block.id,
            parent_tool_use_id: parent,
          });
        }
      }
      break;
    }

    case "user": {
      // Tool results arrive as tool_result blocks on user-role messages.
      const content =
        (message as { message?: { content?: unknown[] } }).message?.content ?? [];
      const parent = parentToolUseID(message);
      for (const block of content as Array<Record<string, unknown>>) {
        if (block.type === "tool_result") {
          notify("message/tool_result", {
            id: block.tool_use_id,
            content: stringifyToolResult(block.content),
            is_error: block.is_error === true,
            parent_tool_use_id: parent,
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

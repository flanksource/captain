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

// streamedBlocks records which content blocks of the assistant message now
// being produced were already sent as deltas, keyed by the block index the
// stream events carry. The SDK repeats each block whole when the message
// completes, and forwarding both would print everything twice.
const streamedBlocks = new Set<number>();

function handleStreamEvent(message: SDKMessage) {
  const event = (message as { event?: Record<string, unknown> }).event;
  if (!event || event.type !== "content_block_delta") {
    return;
  }
  const index = typeof event.index === "number" ? event.index : 0;
  const delta = event.delta as Record<string, unknown> | undefined;
  if (!delta) {
    return;
  }
  if (delta.type === "text_delta" && typeof delta.text === "string" && delta.text) {
    streamedBlocks.add(index);
    notify("message/text", { text: delta.text });
  } else if (
    delta.type === "thinking_delta" &&
    typeof delta.thinking === "string" &&
    delta.thinking
  ) {
    streamedBlocks.add(index);
    notify("message/thinking", { text: delta.thinking });
  }
}

// handleMessage notifies the host of one SDK message. toolName maps the SDK's
// tool name onto the one the host knows the tool by.
export function handleMessage(
  message: SDKMessage,
  toolName: (name: string) => string,
) {
  switch (message.type) {
    case "stream_event":
      handleStreamEvent(message);
      break;

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
      const blocks = content as Array<Record<string, unknown>>;
      for (let index = 0; index < blocks.length; index++) {
        const block = blocks[index];
        const streamed = streamedBlocks.has(index);
        if (block.type === "text") {
          if (!streamed) {
            notify("message/text", { text: block.text });
          }
        } else if (block.type === "thinking") {
          if (!streamed) {
            notify("message/thinking", { text: block.thinking });
          }
        } else if (block.type === "tool_use") {
          notify("message/tool_use", {
            tool: toolName(String(block.name)),
            input: block.input,
            id: block.id,
          });
        }
      }
      // The next message's blocks are indexed from zero again.
      streamedBlocks.clear();
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

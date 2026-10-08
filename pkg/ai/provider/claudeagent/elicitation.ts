// elicitation.ts bridges the SDK's onElicitation to the host's `elicit`
// request, which the host answers through its approval broker.

import type {
  ElicitationRequest,
  ElicitationResult,
} from "@anthropic-ai/claude-agent-sdk";
import { randomUUID } from "crypto";
import { callHost, diag } from "./protocol.js";

// bridgeId names this agent.ts process. The host keys elicitations on it plus
// a per-process counter, because the counter restarts with the process.
const bridgeId = randomUUID();
let nextElicitationId = 1;

// elicitHost forwards an MCP elicitation to the host and maps its answer back.
// Any bridge failure cancels: the server then knows no answer is coming.
export async function elicitHost(
  request: ElicitationRequest,
): Promise<ElicitationResult> {
  const requestId = nextElicitationId++;
  let answer: { action?: string; content?: ElicitationResult["content"] };
  try {
    answer = (await callHost("elicit", {
      bridgeId,
      requestId,
      serverName: request.serverName,
      message: request.message,
      // MCP treats an elicitation without a mode as a form.
      mode: request.mode ?? "form",
      requestedSchema: request.requestedSchema,
      url: request.url,
      elicitationId: request.elicitationId,
    })) as typeof answer;
  } catch (err) {
    diag(`elicit bridge error: ${(err as Error)?.message || err}`);
    return { action: "cancel" };
  }
  switch (answer?.action) {
    case "accept":
      return answer.content
        ? { action: "accept", content: answer.content }
        : { action: "accept" };
    case "decline":
    case "cancel":
      return { action: answer.action };
  }
  diag(`elicit: host answered without a valid action: ${JSON.stringify(answer)}`);
  return { action: "cancel" };
}

import type { SDKUserMessage } from "@anthropic-ai/claude-agent-sdk";

export type JsonRpcId = number | string | null;

export interface PromptAttachment {
  mediaType: string;
  data: string;
  filename?: string;
}

export interface PromptParams {
  text?: string;
  attachments?: PromptAttachment[];
}

export function send(obj: Record<string, unknown>) {
  process.stdout.write(JSON.stringify(obj) + "\n");
}

export function notify(method: string, params: Record<string, unknown>) {
  send({ jsonrpc: "2.0", method, params });
}

export function reply(id: JsonRpcId, result: unknown) {
  send({ jsonrpc: "2.0", id, result });
}

export function replyError(id: JsonRpcId, code: number, message: string) {
  send({ jsonrpc: "2.0", id, error: { code, message } });
}

export function diag(msg: string) {
  process.stderr.write(`[claude-agent] ${msg}\n`);
}

interface HostResponse {
  result?: unknown;
  error?: { code: number; message: string };
}

let nextHostId = 1;
const pendingHostCalls = new Map<string, (resp: HostResponse) => void>();

export function callHost(
  method: string,
  params: Record<string, unknown>,
): Promise<unknown> {
  const id = `agent-${nextHostId++}`;
  return new Promise((resolve, reject) => {
    pendingHostCalls.set(id, (resp) => {
      if (resp.error) {
        reject(new Error(resp.error.message));
      } else {
        resolve(resp.result);
      }
    });
    send({ jsonrpc: "2.0", id, method, params });
  });
}

export function handleResponse(frame: {
  id?: JsonRpcId;
  result?: unknown;
  error?: { code: number; message: string };
}): boolean {
  if (frame.id == null || typeof frame.id !== "string") {
    return false;
  }
  const waiter = pendingHostCalls.get(frame.id);
  if (!waiter) {
    return false;
  }
  pendingHostCalls.delete(frame.id);
  waiter({ result: frame.result, error: frame.error });
  return true;
}

type ClaudeImageMediaType =
  | "image/png"
  | "image/jpeg"
  | "image/gif"
  | "image/webp";

function isClaudeImageMediaType(
  mediaType: string,
): mediaType is ClaudeImageMediaType {
  return ["image/png", "image/jpeg", "image/gif", "image/webp"].includes(
    mediaType,
  );
}

export class TurnQueue implements AsyncIterable<SDKUserMessage> {
  private pending: SDKUserMessage[] = [];
  private waiters: ((result: IteratorResult<SDKUserMessage>) => void)[] = [];
  private ended = false;

  push(params: PromptParams) {
    const content: Exclude<SDKUserMessage["message"]["content"], string> = [];
    if (params.text) {
      content.push({ type: "text", text: params.text });
    }
    for (const attachment of params.attachments ?? []) {
      if (attachment.mediaType === "application/pdf") {
        content.push({
          type: "document",
          source: {
            type: "base64",
            media_type: "application/pdf",
            data: attachment.data,
          },
          title: attachment.filename || undefined,
        });
      } else if (isClaudeImageMediaType(attachment.mediaType)) {
        content.push({
          type: "image",
          source: {
            type: "base64",
            media_type: attachment.mediaType,
            data: attachment.data,
          },
        });
      } else {
        throw new Error(
          `unsupported attachment media type: ${attachment.mediaType}`,
        );
      }
    }
    const message: SDKUserMessage = {
      type: "user",
      message: { role: "user", content },
      parent_tool_use_id: null,
      session_id: "",
    };
    const waiter = this.waiters.shift();
    if (waiter) {
      waiter({ value: message, done: false });
    } else {
      this.pending.push(message);
    }
  }

  end() {
    this.ended = true;
    const waiter = this.waiters.shift();
    if (waiter) {
      waiter({ value: undefined as unknown as SDKUserMessage, done: true });
    }
  }

  [Symbol.asyncIterator](): AsyncIterator<SDKUserMessage> {
    return {
      next: (): Promise<IteratorResult<SDKUserMessage>> => {
        const queued = this.pending.shift();
        if (queued) {
          return Promise.resolve({ value: queued, done: false });
        }
        if (this.ended) {
          return Promise.resolve({
            value: undefined as unknown as SDKUserMessage,
            done: true,
          });
        }
        return new Promise((resolve) => this.waiters.push(resolve));
      },
    };
  }
}

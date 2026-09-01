import type {
  AIPromptRunValue,
  AISpecRuntimeValue,
} from "@flanksource/clicky-ui/ai";
import type {
  ExecutionResponse,
  ResolvedOperation,
} from "@flanksource/clicky-ui/rpc";
import { apiClient } from "./api";

export type PromptSourceFilter = "all" | "embedded" | "local";

export type PromptVariable = {
  name: string;
  type?: string;
  description?: string;
  required?: boolean;
};

export type PromptSummary = {
  id: string;
  name: string;
  description?: string;
  sourceKind: "embedded" | "local" | string;
  sourceId: string;
  source: string;
  path: string;
  relPath: string;
  writable: boolean;
  model?: string;
  backend?: string;
  runtimes?: AISpecRuntimeValue[];
  variables?: PromptVariable[];
  parseError?: string;
  updatedAt?: string;
  /** Content hash echoed back as `baseVersion` on save so a concurrent edit is reported, not clobbered. */
  version?: string;
};

/** One discovered prompt source, as served in the prompt schema document. */
export type PromptSourceInfo = {
  id: string;
  kind: string;
  label: string;
  root?: string;
  writable: boolean;
  implicit?: boolean;
};

export type PromptPreviewResult = {
  id: string;
  name: string;
  model?: string;
  backend?: string;
  user?: string;
  system?: string;
  input?: unknown;
  config?: unknown;
  inputSchema?: Record<string, unknown>;
  inputDefault?: Record<string, unknown>;
  outputSchema?: Record<string, unknown>;
  validationError?: string;
};

export type PromptDetail = PromptSummary & {
  content: string;
  inputSchema?: Record<string, unknown>;
  inputDefault?: Record<string, unknown>;
  outputSchema?: Record<string, unknown>;
  metadata?: Record<string, unknown>;
  run: AIPromptRunValue;
};

export type PromptOps = {
  list?: ResolvedOperation;
  get?: ResolvedOperation;
  create?: ResolvedOperation;
  update?: ResolvedOperation;
  delete?: ResolvedOperation;
  preview?: ResolvedOperation;
  run?: ResolvedOperation;
};

/**
 * Prompts are registered as a clicky entity rather than at a fixed path, so the
 * REST operations are discovered from the OpenAPI document instead of hardcoded.
 */
export function resolvePromptOps(operations: ResolvedOperation[]): PromptOps {
  return {
    list: findPromptOperation(operations, "list"),
    get: findPromptOperation(operations, "get"),
    create: findPromptOperation(operations, "create"),
    update: findPromptOperation(operations, "update"),
    delete: findPromptOperation(operations, "delete"),
    preview: findPromptOperation(operations, "action", "render"),
    run: findPromptOperation(operations, "action", "run"),
  };
}

export function findPromptOperation(
  operations: ResolvedOperation[],
  verb: NonNullable<ResolvedOperation["operation"]["x-clicky"]>["verb"],
  actionName?: string,
) {
  return operations.find((op) => {
    const meta = op.operation["x-clicky"];
    if (!meta || meta.verb !== verb) return false;
    if (actionName && meta.actionName !== actionName) return false;
    const surface = (meta.surface || "").toLowerCase();
    const command = (meta.command || "").toLowerCase();
    const path = op.path.toLowerCase();
    return (
      surface === "prompt" ||
      surface === "prompts" ||
      command === "prompt" ||
      command.startsWith("prompt ") ||
      path.includes("/prompt")
    );
  });
}

export function requiredOperation(
  op: ResolvedOperation | undefined,
  name: string,
) {
  if (!op) throw new Error(`Prompt ${name} operation is not available.`);
  return op;
}

export function unwrapResponse<T>(response: ExecutionResponse): T {
  if (!response.success) {
    throw new Error(response.error || response.output || "Operation failed.");
  }
  return response.parsed as T;
}

export async function fetchPromptList(
  op: ResolvedOperation,
  params: { source: PromptSourceFilter; query: string },
) {
  const response = await apiClient.executeCommand(
    op.path,
    op.method,
    { source: params.source, query: params.query },
    { Accept: "application/json" },
  );
  return unwrapResponse<PromptSummary[]>(response);
}

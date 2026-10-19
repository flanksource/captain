import { apiClient } from "./api";
import type { PromptSelectionSummary } from "./promptData";

export type NamedPrompt = {
  name: string;
  origin: string;
  runId: string;
  sessionId: string;
  queuedAt: string;
};

const EXECUTED_PREFIX = "executed:";

export function executedPromptId(name: string): string {
  if (!name.trim()) throw new Error("Executed prompt name is required.");
  return `${EXECUTED_PREFIX}${name}`;
}

export function executedPromptName(id?: string): string | undefined {
  if (!id?.startsWith(EXECUTED_PREFIX)) return undefined;
  return id.slice(EXECUTED_PREFIX.length) || undefined;
}

export function namedPromptSummary(prompt: NamedPrompt): PromptSelectionSummary {
  return {
    id: executedPromptId(prompt.name),
    name: prompt.name,
    sourceKind: "executed",
    description: `Latest run: ${prompt.origin || "unknown origin"}`,
  };
}

export async function fetchNamedPrompts(): Promise<NamedPrompt[]> {
  const response = await apiClient.executeCommand(
    "/api/captain/prompt/names",
    "GET",
    {},
    { Accept: "application/json" },
  );
  if (!response.success) {
    throw new Error(response.error || "Failed to load executed prompts.");
  }
  if (!Array.isArray(response.parsed)) {
    throw new Error("Executed prompt list is not an array.");
  }
  return response.parsed as NamedPrompt[];
}

import type { AIPromptRunValue } from "@flanksource/clicky-ui/ai";
import type { PromptBatchHandle } from "./hooks/usePromptRunStream";
import type { PromptDetail, PromptPreviewResult } from "./promptData";
import type { PromptSchemaKind } from "./promptSchemaSource";

export const EMPTY_RUN_REQUEST: AIPromptRunValue = {
  variables: {},
  spec: { budget: { timeout: "2h" } },
  chat: true,
};

export const SCRATCH_PROMPT_ID = "__scratch__";

export const SCRATCH_PROMPT: PromptDetail = {
  id: SCRATCH_PROMPT_ID,
  name: "Scratch Prompt",
  description: "Ephemeral prompt",
  sourceKind: "ephemeral",
  sourceId: "ephemeral",
  source: "Ephemeral",
  path: "<ephemeral>",
  relPath: "scratch.prompt",
  writable: false,
  content: "",
  run: EMPTY_RUN_REQUEST,
};

export function isScratchPrompt(detail: PromptDetail | undefined) {
  return detail?.id === SCRATCH_PROMPT_ID;
}

export type PromptDetailState = {
  draft: string;
  /** The content the draft is measured against; updated on save and on reload. */
  savedContent: string;
  /** The detail content this slot was last synced from, so a reload is told apart from a stale detail after save. */
  detailContent: string;
  runRequest: AIPromptRunValue;
  variablesValid: boolean;
  schemaValidity: Record<PromptSchemaKind, boolean>;
  previewResult?: PromptPreviewResult;
  activeRunID?: string;
  activeBatch?: PromptBatchHandle;
  actionError?: string;
  actionLoading?: "save" | "preview" | "run" | "delete";
};

/** One editing slot per prompt id, so switching prompts never discards a draft. */
export type PromptDetailStates = Record<string, PromptDetailState>;

export type PromptDetailStateAction =
  | { type: "draft"; detail?: PromptDetail; value: string }
  | { type: "run-request"; detail?: PromptDetail; value: AIPromptRunValue }
  | { type: "variables-validity"; detail?: PromptDetail; value: boolean }
  | {
      type: "schema-validity";
      detail?: PromptDetail;
      kind: PromptSchemaKind;
      value: boolean;
    }
  | {
      type: "preview-result";
      detail?: PromptDetail;
      value?: PromptPreviewResult;
    }
  | { type: "active-run"; detail?: PromptDetail; value?: string }
  | { type: "active-batch"; detail?: PromptDetail; value?: PromptBatchHandle }
  | { type: "action-error"; detail?: PromptDetail; value?: string }
  | {
      type: "action-loading";
      detail?: PromptDetail;
      value?: PromptDetailState["actionLoading"];
    }
  | { type: "saved"; detail?: PromptDetail; content: string };

export function promptDetailReducer(
  states: PromptDetailStates,
  action: PromptDetailStateAction,
): PromptDetailStates {
  const current = promptDetailStateFor(states, action.detail);
  return { ...states, [stateKey(action.detail)]: applyAction(current, action) };
}

function applyAction(
  current: PromptDetailState,
  action: PromptDetailStateAction,
): PromptDetailState {
  switch (action.type) {
    case "draft":
      return { ...current, draft: action.value };
    case "run-request":
      return { ...current, runRequest: action.value };
    case "variables-validity":
      return { ...current, variablesValid: action.value };
    case "schema-validity":
      return {
        ...current,
        schemaValidity: {
          ...current.schemaValidity,
          [action.kind]: action.value,
        },
      };
    case "preview-result":
      return { ...current, previewResult: action.value };
    case "active-run":
      return { ...current, activeRunID: action.value };
    case "active-batch":
      return { ...current, activeBatch: action.value };
    case "action-error":
      return { ...current, actionError: action.value };
    case "action-loading":
      return { ...current, actionLoading: action.value };
    case "saved":
      return { ...current, draft: action.content, savedContent: action.content };
  }
}

export function initialPromptDetailState(
  detail?: PromptDetail,
): PromptDetailState {
  const runRequest = detail?.run ?? EMPTY_RUN_REQUEST;
  const content = detail?.content ?? "";
  return {
    draft: content,
    savedContent: content,
    detailContent: content,
    runRequest: {
      ...runRequest,
      spec: { ...EMPTY_RUN_REQUEST.spec, ...runRequest.spec },
    },
    variablesValid: true,
    schemaValidity: { input: true, output: true },
    previewResult: undefined,
    activeRunID: undefined,
    activeBatch: undefined,
    actionError: undefined,
    actionLoading: undefined,
  };
}

/**
 * The slot for a prompt. A detail whose content differs from what the slot was
 * synced from is a reload (an edit on disk, or the refetch after a save): an
 * untouched draft adopts the new content, a dirty draft is kept and measured
 * against it. A detail that has not changed never disturbs the slot, so the
 * moment between a save and its refetch does not flash the old content.
 */
export function promptDetailStateFor(
  states: PromptDetailStates,
  detail: PromptDetail | undefined,
): PromptDetailState {
  const state = states[stateKey(detail)];
  if (!state) return initialPromptDetailState(detail);
  const content = detail?.content ?? "";
  if (state.detailContent === content) return state;
  if (state.draft === state.savedContent) return initialPromptDetailState(detail);
  return { ...state, savedContent: content, detailContent: content };
}

export function isPromptDirty(
  state: PromptDetailState,
  detail: PromptDetail | undefined,
) {
  return !isScratchPrompt(detail) && state.draft !== state.savedContent;
}

export function anyPromptDirty(states: PromptDetailStates) {
  return Object.entries(states).some(
    ([key, state]) =>
      key !== "" && key !== SCRATCH_PROMPT_ID && state.draft !== state.savedContent,
  );
}

function stateKey(detail: PromptDetail | undefined) {
  return detail?.id ?? "";
}

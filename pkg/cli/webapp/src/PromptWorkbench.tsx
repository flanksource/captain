import { useMemo, useReducer, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  AppShell,
  Button,
  Combobox,
  Modal,
  SegmentedControl,
  Tabs,
  type AppShellProps,
  type AppShellNavSection,
  type ComboboxOption,
  type KeyPreview,
  type JsonSchemaObject,
  type SecretKind,
  type SecretResource,
} from "@flanksource/clicky-ui/components";
import {
  CodeBlock,
  Icon,
  UiAdd,
  UiCode2,
  UiListTree,
  UiPlay,
  UiRefresh,
  UiSave,
  UiTerminal,
  UiTrash,
} from "@flanksource/clicky-ui/data";
import "@flanksource/clicky-ui/mdx-editor.css";
import { MdxEditorField } from "@flanksource/clicky-ui/mdx-editor";
import {
  PromptRunEditor,
  buildAISpecRuntimePayload,
  type AISpecRuntimePermissionCatalog,
  type AISpecRuntimeValue,
  type ToolMeta,
} from "@flanksource/clicky-ui/ai";
import { type ChatModel } from "@flanksource/clicky-ui/chat";
import {
  useOperations,
  type ResolvedOperation,
} from "@flanksource/clicky-ui/rpc";
import { apiClient } from "./api";
import { PromptRunStream } from "./PromptRunStream";
import { PromptBatchInspector } from "./PromptBatchInspector";
import { PromptSchemaEditor } from "./PromptSchemaEditor";
import { PromptRuntimeRows, validateRuntimeRows } from "./PromptRuntimeRows";
import { RunningPromptsBadge, RunningPromptsRunsTab } from "./RunningPrompts";
import {
  isPromptBatchHandle,
  type PromptBatchHandle,
  type PromptExecutionHandle,
} from "./hooks/usePromptRunStream";
import { CAPTAIN_SIDEBAR_COLLAPSE_KEY } from "./shellHelpers";
import {
  fetchPromptList,
  requiredOperation,
  resolvePromptOps,
  unwrapResponse,
  type PromptSourceFilter,
  type PromptSummary,
} from "./promptData";
import type { PromptSchemaKind } from "./promptSchemaSource";

type Navigate = (to: string, opts?: { replace?: boolean }) => void;

type SourceFilter = PromptSourceFilter;
type DetailTab = "source" | "runner" | "schema" | "runs";

type PromptDetail = PromptSummary & {
  content: string;
  inputSchema?: Record<string, unknown>;
  inputDefault?: Record<string, unknown>;
  outputSchema?: Record<string, unknown>;
  metadata?: Record<string, unknown>;
};

type PromptPreviewResult = {
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

type PromptWorkbenchProps = {
  selectedId?: string;
  onNavigate: Navigate;
  navSections: AppShellNavSection[];
  actions: AppShellProps["actions"];
  search: AppShellProps["search"];
};

const SOURCE_OPTIONS = [
  { id: "all", label: "All" },
  { id: "embedded", label: "Embedded" },
  { id: "local", label: "Local" },
] satisfies Array<{ id: SourceFilter; label: string }>;

// Combobox headers group by *contiguous* `group`, so emit embedded before local
// rather than relying on the server's ordering.
const SOURCE_GROUP_ORDER = ["embedded", "local"];

function promptGroupRank(sourceKind: string) {
  const rank = SOURCE_GROUP_ORDER.indexOf(sourceKind);
  return rank === -1 ? SOURCE_GROUP_ORDER.length : rank;
}

export function promptOptions(
  prompts: PromptSummary[],
  selected?: PromptSummary,
): ComboboxOption[] {
  // The list is server-filtered by the search query, so a selected prompt that
  // no longer matches would otherwise render as a blank input.
  const all =
    selected && !prompts.some((prompt) => prompt.id === selected.id)
      ? [...prompts, selected]
      : prompts;
  return [...all]
    .sort(
      (a, b) =>
        promptGroupRank(a.sourceKind) - promptGroupRank(b.sourceKind) ||
        a.name.localeCompare(b.name),
    )
    .map((prompt) => ({
      value: prompt.id,
      label: prompt.name,
      group: prompt.sourceKind,
      title: prompt.description || prompt.relPath,
    }));
}

const EMPTY_RUNTIME: AISpecRuntimeValue = { budget: { timeout: "2h" } };
const EMPTY_PROMPTS: PromptSummary[] = [];
const EMPTY_MODELS: ChatModel[] = [];
const SCRATCH_PROMPT_ID = "__scratch__";
const SCRATCH_PROMPT: PromptDetail = {
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
};

const AGENT_TOOLS = [
  {
    name: "Read",
    label: "Read",
    group: "Files",
    description: "Read files from the workspace.",
    defaultPermission: "ask",
  },
  {
    name: "Edit",
    label: "Edit",
    group: "Files",
    description: "Apply targeted file edits.",
    defaultPermission: "ask",
  },
  {
    name: "MultiEdit",
    label: "MultiEdit",
    group: "Files",
    description: "Apply multiple edits to one file.",
    defaultPermission: "ask",
  },
  {
    name: "Write",
    label: "Write",
    group: "Files",
    description: "Create or overwrite files.",
    defaultPermission: "ask",
  },
  {
    name: "Glob",
    label: "Glob",
    group: "Search",
    description: "Find files by glob pattern.",
    defaultPermission: "ask",
  },
  {
    name: "Grep",
    label: "Grep",
    group: "Search",
    description: "Search file contents.",
    defaultPermission: "ask",
  },
  {
    name: "LS",
    label: "List",
    group: "Search",
    description: "List directory contents.",
    defaultPermission: "ask",
  },
  {
    name: "Bash",
    label: "Bash",
    group: "Shell",
    description: "Run shell commands.",
    defaultPermission: "ask",
  },
  {
    name: "Task",
    label: "Task",
    group: "Agent",
    description: "Launch a delegated agent task.",
    defaultPermission: "ask",
  },
  {
    name: "TodoWrite",
    label: "Todos",
    group: "Agent",
    description: "Track an agent todo list.",
    defaultPermission: "ask",
  },
  {
    name: "WebFetch",
    label: "Web Fetch",
    group: "Web",
    description: "Fetch content from a URL.",
    defaultPermission: "ask",
  },
  {
    name: "WebSearch",
    label: "Web Search",
    group: "Web",
    description: "Search the web.",
    defaultPermission: "ask",
  },
] satisfies ToolMeta[];

type PromptDetailState = {
  detailId?: string;
  draft: string;
  variables: Record<string, unknown>;
  variablesValid: boolean;
  schemaValidity: Record<PromptSchemaKind, boolean>;
  runtime: AISpecRuntimeValue;
  additionalRuntimes: AISpecRuntimeValue[];
  previewResult?: PromptPreviewResult;
  activeRunID?: string;
  activeBatch?: PromptBatchHandle;
  actionError?: string;
  actionLoading?: "save" | "preview" | "run" | "delete";
};

type PromptDetailStateAction =
  | { type: "draft"; detail?: PromptDetail; value: string }
  | { type: "variables"; detail?: PromptDetail; value: Record<string, unknown> }
  | { type: "variables-validity"; detail?: PromptDetail; value: boolean }
  | {
      type: "schema-validity";
      detail?: PromptDetail;
      kind: PromptSchemaKind;
      value: boolean;
    }
  | { type: "runtime"; detail?: PromptDetail; value: AISpecRuntimeValue }
  | {
      type: "runtime-rows";
      detail?: PromptDetail;
      value: AISpecRuntimeValue[];
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

function promptDetailReducer(
  state: PromptDetailState,
  action: PromptDetailStateAction,
): PromptDetailState {
  const current = promptDetailStateFor(state, action.detail);
  switch (action.type) {
    case "draft":
      return { ...current, draft: action.value };
    case "variables":
      return { ...current, variables: action.value };
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
    case "runtime":
      return { ...current, runtime: action.value };
    case "runtime-rows":
      return {
        ...current,
        runtime: action.value[0] ?? {},
        additionalRuntimes: action.value.slice(1),
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
      return { ...current, draft: action.content };
  }
}

function initialPromptDetailState(detail?: PromptDetail): PromptDetailState {
  const runtimeRows = detail ? runtimeRowsFromPrompt(detail) : [];
  return {
    detailId: detail?.id,
    draft: detail?.content ?? "",
    variables: detail?.inputDefault ?? {},
    variablesValid: true,
    schemaValidity: { input: true, output: true },
    runtime: { ...EMPTY_RUNTIME, ...runtimeRows[0] },
    additionalRuntimes: runtimeRows.slice(1),
    previewResult: undefined,
    activeRunID: undefined,
    activeBatch: undefined,
    actionError: undefined,
    actionLoading: undefined,
  };
}

function promptDetailStateFor(
  state: PromptDetailState,
  detail: PromptDetail | undefined,
): PromptDetailState {
  if (state.detailId !== detail?.id) {
    return initialPromptDetailState(detail);
  }
  return state;
}

export function PromptWorkbench({
  selectedId,
  onNavigate,
  navSections,
  actions,
  search,
}: PromptWorkbenchProps) {
  const {
    operations,
    isLoading: operationsLoading,
    error: operationsError,
  } = useOperations(apiClient);
  const promptOps = useMemo(() => resolvePromptOps(operations), [operations]);
  const [source, setSource] = useState<SourceFilter>("all");
  const [query, setQuery] = useState("");
  const [tab, setTab] = useState<DetailTab>("runner");
  const [detailState, dispatchDetailState] = useReducer(
    promptDetailReducer,
    undefined,
    () => initialPromptDetailState(),
  );
  const [createOpen, setCreateOpen] = useState(false);

  const listQuery = useQuery({
    queryKey: [
      "prompts",
      promptOps.list?.path,
      promptOps.list?.method,
      source,
      query,
    ],
    queryFn: () =>
      fetchPromptList(requiredOperation(promptOps.list, "list"), {
        source,
        query,
      }),
    enabled: Boolean(promptOps.list),
  });
  const promptSchemaQuery = useQuery({
    queryKey: ["prompt-schema"],
    queryFn: fetchPromptSchema,
  });
  const permissionCatalogQuery = useQuery({
    queryKey: ["permission-catalog"],
    queryFn: () => fetchPermissionCatalog(),
  });

  const prompts = listQuery.data ?? EMPTY_PROMPTS;
  const models = promptSchemaQuery.data?.models ?? EMPTY_MODELS;
  const activePromptId = selectedId;

  const selectedSummary = useMemo(
    () =>
      activePromptId
        ? prompts.find((prompt) => prompt.id === activePromptId)
        : SCRATCH_PROMPT,
    [prompts, activePromptId],
  );

  const detailQuery = useQuery({
    queryKey: [
      "prompt",
      promptOps.get?.path,
      promptOps.get?.method,
      activePromptId,
    ],
    queryFn: () =>
      fetchPromptDetail(
        requiredOperation(promptOps.get, "get"),
        String(activePromptId),
      ),
    enabled: Boolean(promptOps.get && activePromptId),
  });

  const detail = activePromptId ? detailQuery.data : SCRATCH_PROMPT;
  const selected = detail ?? selectedSummary;
  const selectedDetailState = promptDetailStateFor(detailState, detail);
  const runtimeRows = [
    selectedDetailState.runtime,
    ...selectedDetailState.additionalRuntimes,
  ];
  const scratch = isScratchPrompt(detail);
  const writableSources = useMemo(
    () => uniqueWritableSources(prompts),
    [prompts],
  );
  const canSave = Boolean(
    detail &&
    !scratch &&
    promptOps.update &&
    selectedDetailState.schemaValidity.input &&
    selectedDetailState.schemaValidity.output &&
    (detail.writable ? selectedDetailState.draft !== detail.content : true),
  );
  const hasSelection = Boolean(detail || activePromptId);
  const operationsReady = Boolean(
    promptOps.list && promptOps.get && promptOps.preview && promptOps.run,
  );

  async function refreshAll() {
    await listQuery.refetch();
    if (activePromptId) await detailQuery.refetch();
  }

  async function saveDraft() {
    if (!detail || scratch || !promptOps.update) return;
    dispatchDetailState({ type: "action-error", detail, value: undefined });
    dispatchDetailState({ type: "action-loading", detail, value: "save" });
    try {
      const saved = await submitPromptOperation<PromptDetail>(
        promptOps.update,
        { id: detail.id },
        { content: selectedDetailState.draft },
      );
      await listQuery.refetch();
      if (saved.id === detail.id) {
        dispatchDetailState({ type: "saved", detail, content: saved.content });
        await detailQuery.refetch();
      } else {
        // Saving a read-only (embedded) prompt forks it to a local copy;
        // switch to the new writable prompt.
        onNavigate(`/prompts/${encodeURIComponent(saved.id)}`);
      }
    } catch (error) {
      dispatchDetailState({
        type: "action-error",
        detail,
        value: errorMessage(error),
      });
    } finally {
      dispatchDetailState({ type: "action-loading", detail, value: undefined });
    }
  }

  async function previewPrompt() {
    if (!detail || !promptOps.preview) return;
    dispatchDetailState({ type: "action-error", detail, value: undefined });
    dispatchDetailState({ type: "action-loading", detail, value: "preview" });
    try {
      const preview = await submitPromptOperation<PromptPreviewResult>(
        promptOps.preview,
        promptActionParams(detail),
        {
          variables: selectedDetailState.variables,
          ...runtimePayload(selectedDetailState.runtime, models),
        },
      );
      dispatchDetailState({ type: "preview-result", detail, value: preview });
      dispatchDetailState({ type: "active-run", detail, value: undefined });
      dispatchDetailState({ type: "active-batch", detail, value: undefined });
    } catch (error) {
      dispatchDetailState({
        type: "action-error",
        detail,
        value: errorMessage(error),
      });
    } finally {
      dispatchDetailState({ type: "action-loading", detail, value: undefined });
    }
  }

  async function runPrompt() {
    if (!detail || !promptOps.run) return;
    dispatchDetailState({ type: "action-error", detail, value: undefined });
    dispatchDetailState({ type: "action-loading", detail, value: "run" });
    try {
      const handle = await submitPromptOperation<PromptExecutionHandle>(
        promptOps.run,
        promptActionParams(detail),
        {
          variables: selectedDetailState.variables,
          ...runtimePayload(selectedDetailState.runtime, models),
          ...(runtimeRows.length > 1
            ? { runtimes: runtimeModelsPayload(runtimeRows, models) }
            : {}),
          chat: promptChatEligible(detail, selectedDetailState.runtime),
        },
      );
      dispatchDetailState({ type: "preview-result", detail, value: undefined });
      if (isPromptBatchHandle(handle)) {
        dispatchDetailState({ type: "active-run", detail, value: undefined });
        dispatchDetailState({ type: "active-batch", detail, value: handle });
      } else {
        dispatchDetailState({ type: "active-batch", detail, value: undefined });
        dispatchDetailState({
          type: "active-run",
          detail,
          value: handle.runId,
        });
      }
      setTab("runner");
    } catch (error) {
      dispatchDetailState({
        type: "action-error",
        detail,
        value: errorMessage(error),
      });
    } finally {
      dispatchDetailState({ type: "action-loading", detail, value: undefined });
    }
  }

  async function deletePrompt() {
    if (!detail || scratch || !promptOps.delete) return;
    dispatchDetailState({ type: "action-error", detail, value: undefined });
    dispatchDetailState({ type: "action-loading", detail, value: "delete" });
    try {
      await executePromptOperation(promptOps.delete, { id: detail.id }, {});
      onNavigate("/prompts", { replace: true });
      await listQuery.refetch();
    } catch (error) {
      dispatchDetailState({
        type: "action-error",
        detail,
        value: errorMessage(error),
      });
    } finally {
      dispatchDetailState({ type: "action-loading", detail, value: undefined });
    }
  }

  return (
    <AppShell
      className="h-screen"
      brand={<div className="text-sm font-semibold">Captain</div>}
      navSections={navSections}
      collapsedStorageKey={CAPTAIN_SIDEBAR_COLLAPSE_KEY}
      actions={actions}
      search={search}
      bodySidebar={
        <PromptSidebar
          source={source}
          onSourceChange={setSource}
          onQueryChange={setQuery}
          prompts={prompts}
          selected={selected}
          loading={listQuery.isLoading || operationsLoading}
          error={listQuery.error ?? operationsError}
          onSelect={(id) => onNavigate(`/prompts/${encodeURIComponent(id)}`)}
          onRefresh={() => void refreshAll()}
          onCreate={() => setCreateOpen(true)}
        />
      }
      bodyHeader={
        <PromptHeader
          prompt={selected}
          loading={Boolean(activePromptId && detailQuery.isLoading)}
          ready={operationsReady}
        />
      }
      bodyActions={
        <div className="flex items-center gap-density-2">
          <RunningPromptsBadge
            onSelectRun={(id) => {
              dispatchDetailState({
                type: "active-batch",
                detail,
                value: undefined,
              });
              dispatchDetailState({ type: "active-run", detail, value: id });
              setTab("runner");
            }}
          />
          {detail?.writable && !scratch && promptOps.delete && (
            <Button
              size="sm"
              variant="ghost"
              loading={selectedDetailState.actionLoading === "delete"}
              onClick={() => void deletePrompt()}
            >
              <Icon icon={UiTrash} className="size-4" />
              Delete
            </Button>
          )}
          {detail && !scratch && promptOps.update && (
            <Button
              size="sm"
              variant="outline"
              disabled={!canSave}
              loading={selectedDetailState.actionLoading === "save"}
              onClick={() => void saveDraft()}
            >
              <Icon icon={UiSave} className="size-4" />
              {detail.writable ? "Save" : "Save to Local"}
            </Button>
          )}
          <Button size="sm" variant="outline" onClick={() => void refreshAll()}>
            <Icon icon={UiRefresh} className="size-4" />
            Refresh
          </Button>
        </div>
      }
      bodySplit={30}
      contentClassName="p-0 overflow-hidden"
    >
      <PromptDetailPane
        detail={detail}
        hasSelection={hasSelection}
        loading={Boolean(activePromptId && detailQuery.isLoading)}
        error={
          detailQuery.error ??
          promptSchemaQuery.error ??
          selectedDetailState.actionError
        }
        tab={tab}
        onTabChange={(next) => setTab(next as DetailTab)}
        draft={selectedDetailState.draft}
        onDraftChange={(value) =>
          dispatchDetailState({ type: "draft", detail, value })
        }
        onSchemaValidityChange={(kind, value) =>
          dispatchDetailState({
            type: "schema-validity",
            detail,
            kind,
            value,
          })
        }
        variables={selectedDetailState.variables}
        variablesValid={selectedDetailState.variablesValid}
        onVariablesChange={(value) =>
          dispatchDetailState({ type: "variables", detail, value })
        }
        onVariablesValidityChange={(value) =>
          dispatchDetailState({ type: "variables-validity", detail, value })
        }
        runtime={selectedDetailState.runtime}
        onRuntimeChange={(value) =>
          dispatchDetailState({ type: "runtime", detail, value })
        }
        runtimeRows={runtimeRows}
        onRuntimeRowsChange={(value) =>
          dispatchDetailState({ type: "runtime-rows", detail, value })
        }
        models={models}
        promptSchema={promptSchemaQuery.data}
        tools={AGENT_TOOLS}
        permissionCatalog={permissionCatalogQuery.data}
        previewResult={selectedDetailState.previewResult}
        activeRunID={selectedDetailState.activeRunID}
        activeBatch={selectedDetailState.activeBatch}
        onEditBatch={() =>
          dispatchDetailState({
            type: "active-batch",
            detail,
            value: undefined,
          })
        }
        onSelectRun={(id) => {
          dispatchDetailState({
            type: "active-batch",
            detail,
            value: undefined,
          });
          dispatchDetailState({ type: "active-run", detail, value: id });
          if (id) setTab("runner");
        }}
        onPreview={() => void previewPrompt()}
        onRun={() => void runPrompt()}
        previewLoading={selectedDetailState.actionLoading === "preview"}
        runLoading={selectedDetailState.actionLoading === "run"}
        previewEnabled={Boolean(promptOps.preview && detail)}
        runEnabled={Boolean(promptOps.run && detail)}
      />
      <CreatePromptModal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        sources={writableSources}
        createOp={promptOps.create}
        seedContent={scratch ? undefined : detail?.content}
        onCreated={(prompt) => {
          setCreateOpen(false);
          void listQuery.refetch();
          onNavigate(`/prompts/${encodeURIComponent(prompt.id)}`);
        }}
      />
    </AppShell>
  );
}

function promptChatEligible(detail: PromptDetail, runtime: AISpecRuntimeValue) {
  return (
    !detail.outputSchema &&
    !runtime.workflow?.verify &&
    !runtime.workflow?.postRun
  );
}

function PromptSidebar({
  source,
  onSourceChange,
  onQueryChange,
  prompts,
  selected,
  loading,
  error,
  onSelect,
  onRefresh,
  onCreate,
}: {
  source: SourceFilter;
  onSourceChange: (source: SourceFilter) => void;
  onQueryChange: (query: string) => void;
  prompts: PromptSummary[];
  selected?: PromptSummary;
  loading: boolean;
  error: unknown;
  onSelect: (id: string) => void;
  onRefresh: () => void;
  onCreate: () => void;
}) {
  const options = useMemo(
    () => promptOptions(prompts, selected),
    [prompts, selected],
  );
  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <div className="shrink-0 space-y-density-2 border-b border-border p-density-3">
        <div className="flex items-center justify-between gap-density-2">
          <div className="text-sm font-semibold">Prompts</div>
          <div className="flex items-center gap-density-1">
            <Button
              size="sm"
              variant="ghost"
              onClick={onRefresh}
              title="Refresh prompts"
            >
              <Icon icon={UiRefresh} className="size-4" />
            </Button>
            <Button
              size="sm"
              variant="ghost"
              onClick={onCreate}
              title="Create prompt"
            >
              <Icon icon={UiAdd} className="size-4" />
            </Button>
          </div>
        </div>
        <Combobox
          value={selected?.id ?? ""}
          onChange={onSelect}
          options={options}
          onSearch={onQueryChange}
          loading={loading}
          allowCustomValue={false}
          placeholder="Select a prompt"
          ariaLabel="Prompt"
          size="sm"
          className="w-full"
        />
        <SegmentedControl
          value={source}
          options={SOURCE_OPTIONS}
          onChange={onSourceChange}
          size="sm"
          aria-label="Prompt source"
          className="w-full"
        />
        <div className="text-xs text-muted-foreground">
          {loading ? "Loading..." : `${prompts.length} prompts`}
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {error ? (
          <div className="p-density-3 text-sm text-destructive">
            {errorMessage(error)}
          </div>
        ) : !selected ? (
          <div className="p-density-3 text-sm text-muted-foreground">
            No prompt selected.
          </div>
        ) : (
          <div className="space-y-density-2 p-density-3">
            <div className="flex min-w-0 items-center justify-between gap-density-2">
              <span className="min-w-0 truncate text-sm font-medium">
                {selected.name}
              </span>
              <span className="shrink-0 rounded border border-border px-1.5 py-0.5 text-[11px] uppercase text-muted-foreground">
                {selected.sourceKind}
              </span>
            </div>
            <div className="truncate text-xs text-muted-foreground">
              {selected.model || selected.backend || "no model"}
              {selected.variables?.length
                ? ` - ${selected.variables.length} vars`
                : ""}
            </div>
            <div className="truncate text-xs text-muted-foreground">
              {selected.relPath}
            </div>
            {selected.description && (
              <div className="text-xs text-muted-foreground">
                {selected.description}
              </div>
            )}
            {selected.parseError && (
              <div className="text-xs text-destructive">
                {selected.parseError}
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

function PromptHeader({
  prompt,
  loading,
  ready,
}: {
  prompt?: PromptSummary;
  loading: boolean;
  ready: boolean;
}) {
  if (!ready) {
    return (
      <div>
        <div className="text-sm font-semibold">Prompt Workbench</div>
        <div className="text-xs text-muted-foreground">
          Loading prompt operations...
        </div>
      </div>
    );
  }
  if (loading && !prompt) {
    return (
      <div className="text-sm text-muted-foreground">Loading prompt...</div>
    );
  }
  if (!prompt) {
    return (
      <div>
        <div className="text-sm font-semibold">Prompt Workbench</div>
        <div className="text-xs text-muted-foreground">
          Select or create a prompt.
        </div>
      </div>
    );
  }
  return (
    <div className="min-w-0">
      <div className="flex min-w-0 flex-wrap items-center gap-density-2">
        <div className="truncate text-sm font-semibold">{prompt.name}</div>
        <span className="rounded border border-border px-1.5 py-0.5 text-[11px] uppercase text-muted-foreground">
          {prompt.sourceKind}
        </span>
        {prompt.writable && (
          <span className="rounded border border-border px-1.5 py-0.5 text-[11px] uppercase text-muted-foreground">
            editable
          </span>
        )}
      </div>
      <div className="mt-1 flex min-w-0 flex-wrap gap-x-density-3 gap-y-1 text-xs text-muted-foreground">
        {prompt.model && <span>{prompt.model}</span>}
        {prompt.backend && <span>{prompt.backend}</span>}
        <span className="max-w-full truncate">{prompt.path}</span>
      </div>
    </div>
  );
}

function PromptDetailPane({
  detail,
  hasSelection,
  loading,
  error,
  tab,
  onTabChange,
  draft,
  onDraftChange,
  onSchemaValidityChange,
  variables,
  variablesValid,
  onVariablesChange,
  onVariablesValidityChange,
  runtime,
  onRuntimeChange,
  runtimeRows,
  onRuntimeRowsChange,
  models,
  promptSchema,
  tools,
  permissionCatalog,
  previewResult,
  activeRunID,
  activeBatch,
  onEditBatch,
  onSelectRun,
  onPreview,
  onRun,
  previewLoading,
  runLoading,
  previewEnabled,
  runEnabled,
}: {
  detail?: PromptDetail;
  hasSelection: boolean;
  loading: boolean;
  error: unknown;
  tab: DetailTab;
  onTabChange: (tab: string) => void;
  draft: string;
  onDraftChange: (value: string) => void;
  onSchemaValidityChange: (kind: PromptSchemaKind, valid: boolean) => void;
  variables: Record<string, unknown>;
  variablesValid: boolean;
  onVariablesChange: (value: Record<string, unknown>) => void;
  onVariablesValidityChange: (valid: boolean) => void;
  runtime: AISpecRuntimeValue;
  onRuntimeChange: (value: AISpecRuntimeValue) => void;
  runtimeRows: AISpecRuntimeValue[];
  onRuntimeRowsChange: (value: AISpecRuntimeValue[]) => void;
  models: ChatModel[];
  promptSchema?: PromptSchemaDoc;
  tools: ToolMeta[];
  permissionCatalog?: AISpecRuntimePermissionCatalog;
  previewResult?: PromptPreviewResult;
  activeRunID?: string;
  activeBatch?: PromptBatchHandle;
  onEditBatch: () => void;
  onSelectRun: (id: string | undefined) => void;
  onPreview: () => void;
  onRun: () => void;
  previewLoading: boolean;
  runLoading: boolean;
  previewEnabled: boolean;
  runEnabled: boolean;
}) {
  if (!hasSelection) {
    return (
      <div className="flex h-full items-center justify-center p-6 text-sm text-muted-foreground">
        Select a prompt.
      </div>
    );
  }
  if (loading && !detail) {
    return (
      <div className="flex h-full items-center justify-center p-6 text-sm text-muted-foreground">
        Loading prompt...
      </div>
    );
  }
  if (!detail) {
    return (
      <div className="h-full overflow-auto p-density-4 text-sm text-destructive">
        {error ? errorMessage(error) : "Prompt not found."}
      </div>
    );
  }

  const scratch = isScratchPrompt(detail);
  const activeTab =
    scratch && (tab === "source" || tab === "schema") ? "runner" : tab;
  const tabs = scratch
    ? [
        { id: "runner", label: "Run", icon: UiPlay },
        { id: "runs", label: "Runs", icon: UiTerminal },
      ]
    : [
        { id: "runner", label: "Run", icon: UiPlay },
        { id: "schema", label: "Schema", icon: UiListTree },
        { id: "source", label: "Source", icon: UiCode2 },
        { id: "runs", label: "Runs", icon: UiTerminal },
      ];
  const schema = scratch
    ? undefined
    : normalizeObjectSchema(detail.inputSchema);
  const backendCliArgs = promptSchema?.backends?.find(
    (backend) => backend.backend === runtime.backend,
  )?.args;
  const promptReady =
    !scratch ||
    Boolean(runtime.prompt?.user?.trim()) ||
    Boolean(runtime.prompt?.attachments?.length);
  const runtimeRowsError = validateRuntimeRows(runtimeRows);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="shrink-0 border-b border-border px-density-4 pt-density-3">
        <Tabs value={activeTab} onChange={onTabChange} tabs={tabs} />
      </div>
      <div className="min-h-0 flex-1 overflow-auto p-density-4">
        {Boolean(error) && (
          <div className="mb-density-3 text-sm text-destructive">
            {errorMessage(error)}
          </div>
        )}
        {activeTab === "runner" && activeBatch ? (
          <PromptBatchInspector handle={activeBatch} onEdit={onEditBatch} />
        ) : activeTab === "source" ? (
          <SourceEditor
            detail={detail}
            draft={draft}
            onDraftChange={onDraftChange}
          />
        ) : activeTab === "runs" ? (
          <RunningPromptsRunsTab
            activeRunID={activeRunID}
            onSelectRun={onSelectRun}
          />
        ) : activeTab === "schema" ? (
          <PromptSchemaEditor
            key={`${detail.id}:${detail.content}`}
            promptId={detail.id}
            source={draft}
            onSourceChange={onDraftChange}
            onValidityChange={onSchemaValidityChange}
          />
        ) : (
          <div className="grid min-h-full gap-density-4 xl:grid-cols-[minmax(340px,0.9fr)_minmax(0,1.1fr)]">
            <div className="space-y-density-4">
              <PromptRunEditor
                key={detail.id}
                value={runtime}
                onChange={onRuntimeChange}
                runtimeControls={
                  <PromptRuntimeRows
                    rows={runtimeRows}
                    models={promptSelectableModels(models)}
                    onChange={onRuntimeRowsChange}
                  />
                }
                models={promptSelectableModels(models)}
                tools={tools}
                secretSelector={CAPTAIN_SECRET_SELECTOR}
                variables={variables}
                onVariablesChange={onVariablesChange}
                onVariablesValidityChange={onVariablesValidityChange}
                enableAttachments
                {...(permissionCatalog ? { permissionCatalog } : {})}
                {...(schema ? { variablesSchema: schema } : {})}
                {...(backendCliArgs
                  ? { cliOptions: { schema: backendCliArgs } }
                  : {})}
                {...(scratch
                  ? {
                      promptLabel: "Scratch prompt",
                      promptPlaceholder: "Write a one-off prompt",
                    }
                  : {})}
              />

              <div className="flex flex-wrap gap-density-2">
                <Button
                  size="sm"
                  variant="outline"
                  loading={previewLoading}
                  disabled={
                    !previewEnabled ||
                    !promptReady ||
                    (!schema && !variablesValid)
                  }
                  onClick={onPreview}
                >
                  <Icon icon={UiCode2} className="size-4" />
                  Render
                </Button>
                <Button
                  size="sm"
                  loading={runLoading}
                  disabled={
                    !runEnabled ||
                    !promptReady ||
                    Boolean(runtimeRowsError) ||
                    (!schema && !variablesValid)
                  }
                  onClick={onRun}
                >
                  <Icon icon={UiPlay} className="size-4" />
                  Run
                </Button>
              </div>
            </div>

            <RunnerOutput
              previewResult={previewResult}
              activeRunID={activeRunID}
            />
          </div>
        )}
      </div>
    </div>
  );
}

function SourceEditor({
  detail,
  draft,
  onDraftChange,
}: {
  detail: PromptDetail;
  draft: string;
  onDraftChange: (value: string) => void;
}) {
  return (
    <div className="space-y-density-2">
      {!detail.writable && (
        <div className="rounded-md border border-border bg-muted/40 px-density-3 py-density-2 text-xs text-muted-foreground">
          This is an embedded prompt. Saving your edits creates a local,
          editable copy.
        </div>
      )}
      <PromptSourceMarkdownEditor
        label="Prompt Source"
        value={draft}
        onChange={onDraftChange}
        minHeight="calc(100vh - 18rem)"
      />
    </div>
  );
}

function PromptSourceMarkdownEditor({
  label,
  value,
  onChange,
  readOnly = false,
  minHeight,
}: {
  label: string;
  value: string;
  onChange?: (value: string) => void;
  readOnly?: boolean;
  minHeight: string | number;
}) {
  return (
    <div className="overflow-hidden rounded-md border border-border bg-card">
      <div className="border-b border-border px-density-3 py-density-2 text-sm font-medium text-card-foreground">
        {label}
      </div>
      <div className="p-density-3" style={{ minHeight }}>
        <MdxEditorField
          value={value}
          onChange={onChange}
          readOnly={readOnly}
          size="xl"
          // Prompt frontmatter is raw runtime source; MDXEditor's structured
          // frontmatter plugin parses transient incomplete YAML while typing.
          codeBlocks={{ defaultLanguage: "markdown" }}
          codeMirror={{
            languages: {
              bash: "Bash",
              handlebars: "Handlebars",
              markdown: "Markdown",
              text: "Text",
              yaml: "YAML",
            },
          }}
          diffMode={{ viewMode: "source" }}
          markdownShortcuts={false}
          contentClassName="font-mono text-xs leading-relaxed"
          textareaClassName="font-mono text-xs leading-relaxed"
          className="min-w-0 rounded-md border border-border bg-background"
          placeholder="Prompt markdown"
        />
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="block space-y-1 text-xs text-muted-foreground">
      <span>{label}</span>
      {children}
    </div>
  );
}

function RunnerOutput({
  previewResult,
  activeRunID,
}: {
  previewResult?: PromptPreviewResult;
  activeRunID?: string;
}) {
  if (activeRunID) {
    return <PromptRunStream runID={activeRunID} />;
  }
  if (previewResult) {
    return (
      <div className="space-y-density-3">
        {previewResult.validationError && (
          <div className="rounded-md border border-destructive/40 bg-destructive/10 p-density-3 text-sm text-destructive">
            {previewResult.validationError}
          </div>
        )}
        <CodeBlock language="markdown" source={previewResult.user || ""} />
        <CodeBlock
          language="json"
          source={JSON.stringify(previewResult.input ?? previewResult, null, 2)}
        />
      </div>
    );
  }
  return (
    <div className="flex min-h-[280px] items-center justify-center rounded-md border border-dashed border-border p-density-6 text-sm text-muted-foreground">
      Render or run the selected prompt.
    </div>
  );
}

function CreatePromptModal({
  open,
  ...props
}: {
  open: boolean;
  onClose: () => void;
  sources: Array<{ id: string; label: string }>;
  createOp?: ResolvedOperation;
  seedContent?: string;
  onCreated: (prompt: PromptDetail) => void;
}) {
  if (!open) return null;
  return <CreatePromptModalForm key={createPromptModalKey(props)} {...props} />;
}

function CreatePromptModalForm({
  onClose,
  sources,
  createOp,
  seedContent,
  onCreated,
}: {
  onClose: () => void;
  sources: Array<{ id: string; label: string }>;
  createOp?: ResolvedOperation;
  seedContent?: string;
  onCreated: (prompt: PromptDetail) => void;
}) {
  const [name, setName] = useState("");
  const [relPath, setRelPath] = useState("");
  const [target, setTarget] = useState(() => sources[0]?.id ?? "");
  const [content, setContent] = useState(
    () => seedContent || defaultPromptContent(""),
  );
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | undefined>();

  async function submit() {
    if (!createOp) return;
    setLoading(true);
    setError(undefined);
    try {
      const created = await submitPromptOperation<PromptDetail>(
        createOp,
        {},
        {
          target,
          name,
          relPath,
          content,
        },
      );
      onCreated(created);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  return (
    <Modal
      open={true}
      onClose={onClose}
      title="New Prompt"
      size="xl"
      footer={
        <div className="flex justify-end gap-density-2">
          <Button size="sm" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            size="sm"
            loading={loading}
            disabled={!createOp}
            onClick={() => void submit()}
          >
            <Icon icon={UiAdd} className="size-4" />
            Create
          </Button>
        </div>
      }
    >
      <div className="space-y-density-3">
        {error && <div className="text-sm text-destructive">{error}</div>}
        <div className="grid gap-density-3 md:grid-cols-3">
          <Field label="Name">
            <input
              value={name}
              onChange={(event) => {
                const next = event.target.value;
                setName(next);
                if (!relPath) setContent(defaultPromptContent(next));
              }}
              className="h-control-h w-full rounded-md border border-border bg-background px-density-3 text-sm outline-none focus:ring-2 focus:ring-ring"
            />
          </Field>
          <Field label="Path">
            <input
              value={relPath}
              onChange={(event) => setRelPath(event.target.value)}
              className="h-control-h w-full rounded-md border border-border bg-background px-density-3 text-sm outline-none focus:ring-2 focus:ring-ring"
              placeholder="name.prompt"
            />
          </Field>
          <Field label="Target">
            <select
              value={target}
              onChange={(event) => setTarget(event.target.value)}
              className="h-control-h w-full rounded-md border border-border bg-background px-density-3 text-sm outline-none focus:ring-2 focus:ring-ring"
            >
              {sources.length === 0 ? (
                <option value="">Default writable source</option>
              ) : (
                sources.map((source) => (
                  <option key={source.id} value={source.id}>
                    {source.label}
                  </option>
                ))
              )}
            </select>
          </Field>
        </div>
        <PromptSourceMarkdownEditor
          label="Prompt Source"
          value={content}
          onChange={setContent}
          minHeight={420}
        />
      </div>
    </Modal>
  );
}

function createPromptModalKey({
  seedContent,
  sources,
}: {
  seedContent?: string;
  sources: Array<{ id: string; label: string }>;
}) {
  return `${sources[0]?.id ?? ""}:${seedContent ?? ""}`;
}

async function fetchPermissionCatalog() {
  const response = await fetch("/api/captain/ai/permissions/catalog", {
    headers: { Accept: "application/json" },
  });
  if (!response.ok) {
    const message = await response.text();
    throw new Error(
      message || `Permission catalog failed with ${response.status}`,
    );
  }
  return (await response.json()) as AISpecRuntimePermissionCatalog;
}

// One backend entry from `captain prompt --schema`: its kind/auth/model status
// plus, for cmux backends, the JSON schema for its extra CLI args.
type PromptSchemaBackend = {
  backend: string;
  kind?: string;
  authenticated?: boolean;
  ready?: boolean;
  models?: string[];
  args?: JsonSchemaObject;
};

// The `captain prompt --schema` document. Only the fields the workbench consumes
// are typed; the served document also carries `spec`/`prompt`/`promptAction`
// schemas and a flat `models` list.
type PromptSchemaDoc = {
  schemaVersion: number;
  backends?: PromptSchemaBackend[];
  models?: ChatModel[];
  spec?: JsonSchemaObject;
};

async function fetchPromptSchema(): Promise<PromptSchemaDoc> {
  const response = await fetch("/api/captain/ai/prompt/schema", {
    headers: { Accept: "application/json" },
  });
  if (!response.ok) {
    const message = await response.text();
    throw new Error(message || `Prompt schema failed with ${response.status}`);
  }
  return (await response.json()) as PromptSchemaDoc;
}

const CAPTAIN_SECRET_SELECTOR = {
  loadResources: fetchSecretResources,
  loadKeyPreview: fetchSecretKeyPreview,
};

async function fetchSecretResources(
  kind: SecretKind,
): Promise<SecretResource[]> {
  const response = await fetch(
    `/api/captain/secrets/resources?kind=${encodeURIComponent(kind)}`,
    {
      headers: { Accept: "application/json" },
    },
  );
  if (!response.ok) return [];
  return (await response.json()) as SecretResource[];
}

async function fetchSecretKeyPreview(
  kind: SecretKind,
  name: string,
): Promise<KeyPreview[]> {
  const response = await fetch(
    `/api/captain/secrets/preview?kind=${encodeURIComponent(kind)}&name=${encodeURIComponent(name)}`,
    { headers: { Accept: "application/json" } },
  );
  if (!response.ok) return [];
  return (await response.json()) as KeyPreview[];
}

async function fetchPromptDetail(op: ResolvedOperation, id: string) {
  const response = await apiClient.executeCommand(
    op.path,
    op.method,
    { id },
    { Accept: "application/json" },
  );
  return unwrapResponse<PromptDetail>(response);
}

async function submitPromptOperation<T>(
  op: ResolvedOperation,
  params: Record<string, string>,
  body: Record<string, unknown>,
) {
  const response = await apiClient.submitForm!(
    resolveOperationPath(op.path, params),
    op.method,
    body,
    { Accept: "application/json" },
  );
  return unwrapResponse<T>(response);
}

async function executePromptOperation(
  op: ResolvedOperation,
  params: Record<string, string>,
  body: Record<string, string>,
) {
  const response = await apiClient.executeCommand(
    op.path,
    op.method,
    paramsWithPathValues(op.path, params, body),
    { Accept: "application/json" },
  );
  return unwrapResponse<unknown>(response);
}

function resolveOperationPath(path: string, params: Record<string, string>) {
  let next = path;
  for (const [key, value] of Object.entries(params)) {
    if (value) {
      next = next.replace(`{${key}}`, encodeURIComponent(value));
    } else {
      next = next.replace(new RegExp(`/\\{${key}\\}`, "g"), "");
      next = next.replace(`{${key}}`, "");
    }
  }
  return next.replace(/\/{2,}/g, "/");
}

function paramsWithPathValues(
  path: string,
  pathParams: Record<string, string>,
  params: Record<string, string>,
) {
  const next = { ...params };
  for (const key of path.matchAll(/\{([^{}]+)\}/g)) {
    const name = key[1];
    if (name && pathParams[name]) next[name] = pathParams[name];
  }
  return next;
}

function uniqueWritableSources(prompts: PromptSummary[]) {
  const seen = new Set<string>();
  const out: Array<{ id: string; label: string }> = [];
  for (const prompt of prompts) {
    if (!prompt.writable || seen.has(prompt.sourceId)) continue;
    seen.add(prompt.sourceId);
    out.push({ id: prompt.sourceId, label: prompt.source });
  }
  return out;
}

function isScratchPrompt(detail: PromptDetail | undefined) {
  return detail?.id === SCRATCH_PROMPT_ID;
}

function promptActionParams(detail: PromptDetail) {
  return { id: isScratchPrompt(detail) ? "" : detail.id };
}

function normalizeObjectSchema(
  schema: Record<string, unknown> | undefined,
): JsonSchemaObject | undefined {
  if (!schema || typeof schema !== "object") return undefined;
  const properties = schema.properties;
  if (
    !properties ||
    typeof properties !== "object" ||
    Array.isArray(properties)
  )
    return undefined;
  return {
    ...schema,
    type: "object",
    properties: properties as JsonSchemaObject["properties"],
  } as JsonSchemaObject;
}

// runtime is the single source of truth: the inline PromptRunEditor and its
// "Edit spec" modal both edit this one AISpecRuntimeValue, so the payload is
// just the compacted spec (plus catalog model/backend normalization).
function runtimePayload(runtime: AISpecRuntimeValue, models: ChatModel[]) {
  return normalizeSpecRuntimePayload(
    buildAISpecRuntimePayload(runtime),
    models,
    runtime.backend,
  );
}

export function runtimeModelsPayload(
  runtimes: AISpecRuntimeValue[],
  models: ChatModel[],
) {
  return runtimes.map((runtime) => {
    const selected = normalizeRuntimeModel(
      runtime.model || "",
      models,
      runtime.backend,
    );
    return {
      model: selected.model,
      backend: selected.backend || runtime.backend,
      ...(runtime.id ? { id: runtime.id } : {}),
      ...(runtime.effort ? { effort: runtime.effort } : {}),
      ...(runtime.temperature !== undefined
        ? { temperature: runtime.temperature }
        : {}),
      ...(runtime.noCache ? { noCache: true } : {}),
      ...(runtime.fallbacks?.length ? { fallbacks: runtime.fallbacks } : {}),
    };
  });
}

function normalizeSpecRuntimePayload(
  payload: Record<string, unknown>,
  models: ChatModel[],
  backend?: string,
) {
  const spec = payload.spec;
  if (!spec || typeof spec !== "object" || Array.isArray(spec)) return payload;
  const specRecord = { ...(spec as Record<string, unknown>) };
  if (typeof specRecord.model === "string") {
    const selected = normalizeRuntimeModel(
      specRecord.model,
      models,
      typeof specRecord.backend === "string" ? specRecord.backend : backend,
    );
    if (selected.model && selected.model !== specRecord.model) {
      if (typeof specRecord.id !== "string" || !specRecord.id.trim()) {
        specRecord.id = specRecord.model;
      }
      specRecord.model = selected.model;
    }
    if (
      selected.backend &&
      (typeof specRecord.backend !== "string" || !specRecord.backend.trim())
    ) {
      specRecord.backend = selected.backend;
    }
  }
  return { ...payload, spec: specRecord };
}

// Seeds the first runtime row from the prompt so adding a comparison preserves
// the prompt's existing model as an explicit participant.
function runtimeSelectionFromPrompt(prompt: PromptSummary): AISpecRuntimeValue {
  const backend =
    prompt.backend?.trim() || inferBackendFromModel(prompt.model || "");
  return {
    ...(backend ? { backend } : {}),
    ...(prompt.model?.trim() ? { model: prompt.model.trim() } : {}),
  };
}

export function runtimeRowsFromPrompt(
  prompt: PromptSummary,
): AISpecRuntimeValue[] {
  if (prompt.runtimes?.length) {
    return prompt.runtimes.map(
      ({ model, id, backend, temperature, effort, noCache, fallbacks }) => ({
        model,
        id,
        backend,
        temperature,
        effort,
        noCache,
        fallbacks,
      }),
    );
  }
  return [runtimeSelectionFromPrompt(prompt)];
}

function inferBackendFromModel(model: string) {
  const value = model.trim().toLowerCase();
  if (!value) return "";
  if (value.startsWith("anthropic/")) return "anthropic";
  if (value.startsWith("openai/")) return "openai";
  if (value.startsWith("googleai/")) return "gemini";
  if (value.startsWith("deepseek/") || value.startsWith("deepseek-"))
    return "deepseek";
  if (value.startsWith("claude-agent-")) return "claude-agent";
  if (value.startsWith("claude-code-")) return "claude-cli";
  if (value.startsWith("codex-agent-") || value.startsWith("codex"))
    return "codex-agent";
  if (value.startsWith("gemini-cli-")) return "gemini-cli";
  if (value.startsWith("claude-")) return "anthropic";
  if (value.startsWith("gemini-") || value.startsWith("models/gemini-"))
    return "gemini";
  if (
    value.startsWith("gpt-") ||
    value.startsWith("o1") ||
    value.startsWith("o3") ||
    value.startsWith("o4")
  ) {
    return "openai";
  }
  return "";
}

function promptSelectableModels(models: ChatModel[]) {
  return models.map((model) =>
    model.configured === false ? { ...model, configured: true } : model,
  );
}

function normalizeRuntimeModel(
  model: string,
  models: ChatModel[],
  backend?: string,
) {
  const id = model.trim();
  if (!id) return { model: "", backend: "" };

  const selected =
    models.find(
      (entry) => entry.id === id && modelSupportsBackend(entry, backend),
    ) ?? models.find((entry) => entry.id === id);
  if (!selected) return { model: id, backend: "" };

  const selectedBackend = backendForModel(selected, backend);
  if (
    selected.provider === "anthropic" ||
    selected.provider === "openai" ||
    selected.provider === "googleai" ||
    selected.provider === "deepseek"
  ) {
    return { model: stripProviderPrefix(id), backend: selectedBackend };
  }
  if (
    (selected.provider === "codex-cli" ||
      selected.provider === "codex-agent") &&
    id.startsWith("codex-")
  ) {
    return { model: id.slice("codex-".length), backend: selectedBackend };
  }
  return { model: id, backend: selectedBackend };
}

function backendForModel(model: ChatModel, preferredBackend?: string) {
  if (preferredBackend && modelSupportsBackend(model, preferredBackend)) {
    return preferredBackend;
  }
  const backend = model.backends?.find((candidate) => candidate.trim());
  return backend ?? providerToBackend(model.provider);
}

function modelSupportsBackend(model: ChatModel, backend?: string) {
  if (!backend) return true;
  const backends = model.backends?.filter(Boolean);
  if (!backends || backends.length === 0) return true;
  return backends.includes(backend);
}

function providerToBackend(provider: string) {
  switch (provider) {
    case "googleai":
      return "gemini";
    case "anthropic":
    case "openai":
    case "deepseek":
    case "claude-agent":
    case "claude-cli":
    case "codex-cli":
    case "codex-agent":
    case "gemini-cli":
      return provider;
    default:
      return "";
  }
}

function stripProviderPrefix(model: string) {
  const slash = model.indexOf("/");
  return slash >= 0 ? model.slice(slash + 1) : model;
}

function defaultPromptContent(name: string) {
  const promptName = name.trim() || "new prompt";
  return `---
name: ${JSON.stringify(promptName)}
description: ""
input:
  schema:
    input: string
---
{{role "user"}}
{{input}}
`;
}

function errorMessage(error: unknown) {
  if (error instanceof Error) return error.message;
  if (typeof error === "string") return error;
  return "Unexpected error.";
}

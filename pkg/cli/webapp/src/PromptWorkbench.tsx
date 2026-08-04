import { useMemo, useReducer, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  AppShell,
  Button,
  Combobox,
  SegmentedControl,
  Tabs,
  type AppShellProps,
  type AppShellNavSection,
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
  UiTerminal,
  UiTrash,
} from "@flanksource/clicky-ui/data";
import {
  PromptRunEditor,
  familiesFromRuntimeCatalog,
  type AIPromptRunValue,
  type AISpecRuntimePermissionCatalog,
  type RuntimeCatalogFamily,
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
  type PromptDetail,
  type PromptSourceFilter,
  type PromptSummary,
} from "./promptData";
import type { PromptSchemaKind } from "./promptSchemaSource";
import {
  mergePromptModelCatalogs,
  promptOptions,
} from "./promptWorkbenchHelpers";
import {
  PromptSourceMarkdownEditor,
  PromptWriteAction,
  PromptWriteModal,
  type PromptWriteInput,
  type PromptWriteMode,
} from "./PromptWriteModal";

type Navigate = (to: string, opts?: { replace?: boolean }) => void;

type SourceFilter = PromptSourceFilter;
type DetailTab = "source" | "runner" | "schema" | "runs";

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

const EMPTY_RUN_REQUEST: AIPromptRunValue = {
  variables: {},
  spec: { budget: { timeout: "2h" } },
  chat: true,
};
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
  run: EMPTY_RUN_REQUEST,
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
  runRequest: AIPromptRunValue;
  variablesValid: boolean;
  schemaValidity: Record<PromptSchemaKind, boolean>;
  previewResult?: PromptPreviewResult;
  activeRunID?: string;
  activeBatch?: PromptBatchHandle;
  actionError?: string;
  actionLoading?: "save" | "preview" | "run" | "delete";
};

type PromptDetailStateAction =
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

function promptDetailReducer(
  state: PromptDetailState,
  action: PromptDetailStateAction,
): PromptDetailState {
  const current = promptDetailStateFor(state, action.detail);
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
      return { ...current, draft: action.content };
  }
}

function initialPromptDetailState(detail?: PromptDetail): PromptDetailState {
  const runRequest = detail?.run ?? EMPTY_RUN_REQUEST;
  return {
    detailId: detail?.id,
    draft: detail?.content ?? "",
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

function promptDetailStateFor(
  state: PromptDetailState,
  detail: PromptDetail | undefined,
): PromptDetailState {
  if (state.detailId !== detail?.id) {
    return initialPromptDetailState(detail);
  }
  return state;
}

export function PromptWorkbench(props: PromptWorkbenchProps) {
  return usePromptWorkbenchView(props);
}

function usePromptWorkbenchView({
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
  const [writeMode, setWriteMode] = useState<PromptWriteMode>();

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
  const modelCatalogQuery = useQuery({
    queryKey: ["chat-model-catalog"],
    queryFn: () => fetchCatalog<ChatModel>("/api/chat/models", "Model catalog"),
  });
  const runtimeCatalogQuery = useQuery({
    queryKey: ["chat-runtime-catalog"],
    queryFn: () =>
      fetchCatalog<RuntimeCatalogFamily>(
        "/api/chat/runtimes",
        "Runtime catalog",
      ),
  });
  const permissionCatalogQuery = useQuery({
    queryKey: ["permission-catalog"],
    queryFn: () => fetchPermissionCatalog(),
  });

  const prompts = listQuery.data ?? EMPTY_PROMPTS;
  const models = useMemo(
    () =>
      mergePromptModelCatalogs(
        promptSchemaQuery.data?.models ?? EMPTY_MODELS,
        modelCatalogQuery.data ?? EMPTY_MODELS,
      ),
    [modelCatalogQuery.data, promptSchemaQuery.data?.models],
  );
  const runtimeCatalog =
    runtimeCatalogQuery.data ?? promptSchemaQuery.data?.runtimes;
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
  const scratch = isScratchPrompt(detail);
  const writableSources = useMemo(
    () => uniqueWritableSources(prompts),
    [prompts],
  );
  const canSave = Boolean(
    detail &&
    !scratch &&
    detail.writable &&
    promptOps.update &&
    selectedDetailState.schemaValidity.input &&
    selectedDetailState.schemaValidity.output &&
    selectedDetailState.draft !== detail.content,
  );
  const canSaveAs = Boolean(
    detail &&
    !scratch &&
    !detail.writable &&
    promptOps.create &&
    selectedDetailState.schemaValidity.input &&
    selectedDetailState.schemaValidity.output,
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
    if (!detail?.writable || scratch || !promptOps.update) return;
    dispatchDetailState({ type: "action-error", detail, value: undefined });
    dispatchDetailState({ type: "action-loading", detail, value: "save" });
    try {
      const saved = await submitPromptOperation<PromptDetail>(
        promptOps.update,
        { id: detail.id },
        { content: selectedDetailState.draft },
      );
      await listQuery.refetch();
      dispatchDetailState({ type: "saved", detail, content: saved.content });
      await detailQuery.refetch();
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

  async function createPromptCopy(input: PromptWriteInput) {
    const create = requiredOperation(promptOps.create, "create");
    const created = await submitPromptOperation<PromptDetail>(
      create,
      {},
      { ...input },
    );
    setWriteMode(undefined);
    await listQuery.refetch();
    onNavigate(`/prompts/${encodeURIComponent(created.id)}`);
  }

  async function previewPrompt() {
    if (!detail || !promptOps.preview) return;
    dispatchDetailState({ type: "action-error", detail, value: undefined });
    dispatchDetailState({ type: "action-loading", detail, value: "preview" });
    try {
      const preview = await submitPromptOperation<PromptPreviewResult>(
        promptOps.preview,
        promptActionParams(detail),
        selectedDetailState.runRequest,
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
        selectedDetailState.runRequest,
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
    <>
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
            onCreate={() => setWriteMode("create")}
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
            {detail &&
              !scratch &&
              ((detail.writable && promptOps.update) ||
                (!detail.writable && promptOps.create)) && (
                <PromptWriteAction
                  writable={detail.writable}
                  disabled={detail.writable ? !canSave : !canSaveAs}
                  loading={
                    detail.writable &&
                    selectedDetailState.actionLoading === "save"
                  }
                  onClick={() => {
                    if (detail.writable) {
                      void saveDraft();
                    } else {
                      setWriteMode("save-as");
                    }
                  }}
                />
              )}
            <Button
              size="sm"
              variant="outline"
              onClick={() => void refreshAll()}
            >
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
            modelCatalogQuery.error ??
            runtimeCatalogQuery.error ??
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
          variablesValid={selectedDetailState.variablesValid}
          onVariablesValidityChange={(value) =>
            dispatchDetailState({ type: "variables-validity", detail, value })
          }
          runRequest={selectedDetailState.runRequest}
          onRunRequestChange={(value) =>
            dispatchDetailState({ type: "run-request", detail, value })
          }
          models={models}
          runtimeCatalog={runtimeCatalog}
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
      </AppShell>
      <PromptWriteModal
        open={writeMode !== undefined}
        mode={writeMode ?? "create"}
        onClose={() => setWriteMode(undefined)}
        sources={writableSources}
        onSubmit={createPromptCopy}
        {...(writeMode === "save-as" && detail
          ? {
              initialName: detail.name,
              initialContent: selectedDetailState.draft,
            }
          : !scratch && detail
            ? { initialContent: detail.content }
            : {})}
      />
    </>
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
  variablesValid,
  onVariablesValidityChange,
  runRequest,
  onRunRequestChange,
  models,
  runtimeCatalog,
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
  variablesValid: boolean;
  onVariablesValidityChange: (valid: boolean) => void;
  runRequest: AIPromptRunValue;
  onRunRequestChange: (value: AIPromptRunValue) => void;
  models: ChatModel[];
  runtimeCatalog?: RuntimeCatalogFamily[];
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
    (backend) => backend.backend === runRequest.spec?.backend,
  )?.args;
  const runtimeFamilies = familiesFromRuntimeCatalog(runtimeCatalog);
  const promptReady =
    !scratch ||
    Boolean(runRequest.spec?.prompt?.user?.trim()) ||
    Boolean(runRequest.spec?.prompt?.attachments?.length);

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
                value={runRequest}
                onChange={onRunRequestChange}
                families={runtimeFamilies}
                models={models}
                tools={tools}
                secretSelector={CAPTAIN_SECRET_SELECTOR}
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
                    !runEnabled || !promptReady || (!schema && !variablesValid)
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
          This is an embedded prompt. Use Save as… to create a local, editable
          copy.
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
  /**
   * The provider×mode catalog the runtime picker renders. The server projects it
   * from its model registry and has already dropped the disabled entries, so the
   * client never re-derives which backends exist.
   */
  runtimes?: RuntimeCatalogFamily[];
  /** The enabled effort tiers, for models the catalog does not describe. */
  efforts?: string[];
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

async function fetchCatalog<T>(endpoint: string, label: string): Promise<T[]> {
  const response = await fetch(endpoint, {
    headers: { Accept: "application/json" },
  });
  if (!response.ok) {
    const message = await response.text();
    throw new Error(message || `${label} failed with ${response.status}`);
  }
  const data: unknown = await response.json();
  if (!Array.isArray(data)) throw new Error(`${label} must be an array`);
  return data as T[];
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

function errorMessage(error: unknown) {
  if (error instanceof Error) return error.message;
  if (typeof error === "string") return error;
  return "Unexpected error.";
}

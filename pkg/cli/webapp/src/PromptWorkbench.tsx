import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  AppShell,
  Button,
  Modal,
  SearchInput,
  SegmentedControl,
  Tabs,
  type AppShellNavSection,
  type KeyPreview,
  type JsonSchemaObject,
  type SecretKind,
  type SecretResource,
} from "@flanksource/clicky-ui/components";
import {
  CodeBlock,
  Icon,
  SchemaViewer,
  UiAdd,
  UiCode2,
  UiFileSearch,
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
  type ExecutionResponse,
  type ResolvedOperation,
} from "@flanksource/clicky-ui/rpc";
import { apiClient } from "./api";
import { PromptRunStream } from "./PromptRunStream";
import { RunningPrompts } from "./RunningPrompts";
import type { PromptRunHandle } from "./hooks/usePromptRunStream";
import { CAPTAIN_SIDEBAR_COLLAPSE_KEY } from "./shell";

type Navigate = (to: string, opts?: { replace?: boolean }) => void;

type SourceFilter = "all" | "embedded" | "local";
type DetailTab = "source" | "runner" | "schema" | "runs";

type PromptVariable = {
  name: string;
  type?: string;
  description?: string;
  required?: boolean;
};

type PromptSummary = {
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
  variables?: PromptVariable[];
  parseError?: string;
  updatedAt?: string;
};

type PromptDetail = PromptSummary & {
  content: string;
  inputSchema?: Record<string, unknown>;
  inputDefault?: Record<string, unknown>;
  outputSchema?: Record<string, unknown>;
  metadata?: Record<string, unknown>;
};

type PromptRenderResult = {
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

type PromptOps = {
  list?: ResolvedOperation;
  get?: ResolvedOperation;
  create?: ResolvedOperation;
  update?: ResolvedOperation;
  delete?: ResolvedOperation;
  render?: ResolvedOperation;
  run?: ResolvedOperation;
};

type PromptWorkbenchProps = {
  selectedId?: string;
  onNavigate: Navigate;
  navSections: AppShellNavSection[];
  actions: ReactNode;
};

const SOURCE_OPTIONS = [
  { id: "all", label: "All" },
  { id: "embedded", label: "Embedded" },
  { id: "local", label: "Local" },
] satisfies Array<{ id: SourceFilter; label: string }>;

const EMPTY_RUNTIME: AISpecRuntimeValue = { budget: { timeout: "2h" } };

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

export function PromptWorkbench({
  selectedId,
  onNavigate,
  navSections,
  actions,
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
  const [draft, setDraft] = useState("");
  const [variables, setVariables] = useState<Record<string, unknown>>({});
  const [variablesValid, setVariablesValid] = useState(true);
  const [runtime, setRuntime] = useState<AISpecRuntimeValue>(EMPTY_RUNTIME);
  const [renderResult, setRenderResult] = useState<
    PromptRenderResult | undefined
  >();
  const [activeRunID, setActiveRunID] = useState<string | undefined>();
  const [actionError, setActionError] = useState<string | undefined>();
  const [actionLoading, setActionLoading] = useState<
    "save" | "render" | "run" | "delete" | undefined
  >();
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
  const modelsQuery = useQuery({
    queryKey: ["chat-models"],
    queryFn: fetchChatModels,
  });
  const permissionCatalogQuery = useQuery({
    queryKey: ["permission-catalog"],
    queryFn: () => fetchPermissionCatalog(),
  });

  const prompts = listQuery.data ?? [];
  const models = modelsQuery.data ?? [];

  useEffect(() => {
    if (selectedId || prompts.length === 0) return;
    onNavigate(`/prompts/${encodeURIComponent(prompts[0].id)}`, {
      replace: true,
    });
  }, [onNavigate, prompts, selectedId]);

  const selectedSummary = useMemo(
    () => prompts.find((prompt) => prompt.id === selectedId),
    [prompts, selectedId],
  );

  const detailQuery = useQuery({
    queryKey: [
      "prompt",
      promptOps.get?.path,
      promptOps.get?.method,
      selectedId,
    ],
    queryFn: () =>
      fetchPromptDetail(
        requiredOperation(promptOps.get, "get"),
        String(selectedId),
      ),
    enabled: Boolean(promptOps.get && selectedId),
  });

  const selected = detailQuery.data ?? selectedSummary;
  const detail = detailQuery.data;
  const writableSources = useMemo(
    () => uniqueWritableSources(prompts),
    [prompts],
  );
  const canSave = Boolean(
    detail &&
    promptOps.update &&
    (detail.writable ? draft !== detail.content : true),
  );
  const hasSelection = Boolean(selectedId);
  const operationsReady = Boolean(
    promptOps.list && promptOps.get && promptOps.render && promptOps.run,
  );

  useEffect(() => {
    if (!detail) return;
    setDraft(detail.content);
    const defaults = detail.inputDefault ?? {};
    setVariables(defaults);
    setVariablesValid(true);
    setRuntime({ ...EMPTY_RUNTIME, ...runtimeSelectionFromPrompt(detail) });
    setRenderResult(undefined);
    setActiveRunID(undefined);
    setActionError(undefined);
  }, [detail?.id]);

  async function refreshAll() {
    await listQuery.refetch();
    if (selectedId) await detailQuery.refetch();
  }

  async function saveDraft() {
    if (!detail || !promptOps.update) return;
    setActionError(undefined);
    setActionLoading("save");
    try {
      const saved = await submitPromptOperation<PromptDetail>(
        promptOps.update,
        { id: detail.id },
        { content: draft },
      );
      await listQuery.refetch();
      if (saved.id === detail.id) {
        setDraft(saved.content);
        await detailQuery.refetch();
      } else {
        // Saving a read-only (embedded) prompt forks it to a local copy;
        // switch to the new writable prompt.
        onNavigate(`/prompts/${encodeURIComponent(saved.id)}`);
      }
    } catch (error) {
      setActionError(errorMessage(error));
    } finally {
      setActionLoading(undefined);
    }
  }

  async function renderPrompt() {
    if (!detail || !promptOps.render) return;
    setActionError(undefined);
    setActionLoading("render");
    try {
      const rendered = await submitPromptOperation<PromptRenderResult>(
        promptOps.render,
        { id: detail.id },
        { variables, ...runtimePayload(runtime, models) },
      );
      setRenderResult(rendered);
      setActiveRunID(undefined);
    } catch (error) {
      setActionError(errorMessage(error));
    } finally {
      setActionLoading(undefined);
    }
  }

  async function runPrompt() {
    if (!detail || !promptOps.run) return;
    setActionError(undefined);
    setActionLoading("run");
    try {
      const handle = await submitPromptOperation<PromptRunHandle>(
        promptOps.run,
        { id: detail.id },
        { variables, ...runtimePayload(runtime, models) },
      );
      setRenderResult(undefined);
      setActiveRunID(handle.runId);
      setTab("runner");
    } catch (error) {
      setActionError(errorMessage(error));
    } finally {
      setActionLoading(undefined);
    }
  }

  async function deletePrompt() {
    if (!detail || !promptOps.delete) return;
    setActionError(undefined);
    setActionLoading("delete");
    try {
      await executePromptOperation(promptOps.delete, { id: detail.id }, {});
      onNavigate("/prompts", { replace: true });
      await listQuery.refetch();
    } catch (error) {
      setActionError(errorMessage(error));
    } finally {
      setActionLoading(undefined);
    }
  }

  return (
    <AppShell
      className="h-screen"
      brand={<div className="text-sm font-semibold">Captain</div>}
      navSections={navSections}
      collapsedStorageKey={CAPTAIN_SIDEBAR_COLLAPSE_KEY}
      actions={actions}
      bodySidebar={
        <PromptSidebar
          source={source}
          onSourceChange={setSource}
          query={query}
          onQueryChange={setQuery}
          prompts={prompts}
          selectedId={selectedId}
          loading={listQuery.isLoading || operationsLoading}
          error={listQuery.error ?? operationsError}
          onSelect={(prompt) =>
            onNavigate(`/prompts/${encodeURIComponent(prompt.id)}`)
          }
          onRefresh={() => void refreshAll()}
          onCreate={() => setCreateOpen(true)}
        />
      }
      bodyHeader={
        <PromptHeader
          prompt={selected}
          loading={detailQuery.isLoading}
          ready={operationsReady}
        />
      }
      bodyActions={
        <div className="flex items-center gap-density-2">
          <RunningPrompts.Badge
            onSelectRun={(id) => {
              setActiveRunID(id);
              setTab("runner");
            }}
          />
          {detail?.writable && promptOps.delete && (
            <Button
              size="sm"
              variant="ghost"
              loading={actionLoading === "delete"}
              onClick={() => void deletePrompt()}
            >
              <Icon icon={UiTrash} className="size-4" />
              Delete
            </Button>
          )}
          {detail && promptOps.update && (
            <Button
              size="sm"
              variant="outline"
              disabled={!canSave}
              loading={actionLoading === "save"}
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
        loading={detailQuery.isLoading}
        error={detailQuery.error ?? actionError}
        tab={tab}
        onTabChange={(next) => setTab(next as DetailTab)}
        draft={draft}
        onDraftChange={setDraft}
        variables={variables}
        variablesValid={variablesValid}
        onVariablesChange={setVariables}
        onVariablesValidityChange={setVariablesValid}
        runtime={runtime}
        onRuntimeChange={setRuntime}
        models={models}
        tools={AGENT_TOOLS}
        permissionCatalog={permissionCatalogQuery.data}
        renderResult={renderResult}
        activeRunID={activeRunID}
        onSelectRun={(id) => {
          setActiveRunID(id);
          if (id) setTab("runner");
        }}
        onRender={() => void renderPrompt()}
        onRun={() => void runPrompt()}
        renderLoading={actionLoading === "render"}
        runLoading={actionLoading === "run"}
        renderEnabled={Boolean(promptOps.render && detail)}
        runEnabled={Boolean(promptOps.run && detail)}
      />
      <CreatePromptModal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        sources={writableSources}
        createOp={promptOps.create}
        seedContent={detail?.content}
        onCreated={(prompt) => {
          setCreateOpen(false);
          void listQuery.refetch();
          onNavigate(`/prompts/${encodeURIComponent(prompt.id)}`);
        }}
      />
    </AppShell>
  );
}

function PromptSidebar({
  source,
  onSourceChange,
  query,
  onQueryChange,
  prompts,
  selectedId,
  loading,
  error,
  onSelect,
  onRefresh,
  onCreate,
}: {
  source: SourceFilter;
  onSourceChange: (source: SourceFilter) => void;
  query: string;
  onQueryChange: (query: string) => void;
  prompts: PromptSummary[];
  selectedId?: string;
  loading: boolean;
  error: unknown;
  onSelect: (prompt: PromptSummary) => void;
  onRefresh: () => void;
  onCreate: () => void;
}) {
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
        <SearchInput
          value={query}
          onChange={onQueryChange}
          placeholder="Search prompts"
          shortcut={null}
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
        ) : prompts.length === 0 && !loading ? (
          <div className="p-density-3 text-sm text-muted-foreground">
            No prompts found.
          </div>
        ) : (
          <div className="divide-y divide-border">
            {prompts.map((prompt) => {
              const active = prompt.id === selectedId;
              return (
                <button
                  key={prompt.id}
                  type="button"
                  onClick={() => onSelect(prompt)}
                  className={[
                    "block w-full px-density-3 py-density-2 text-left transition-colors",
                    active
                      ? "bg-accent text-accent-foreground"
                      : "hover:bg-muted/60",
                  ].join(" ")}
                >
                  <div className="flex min-w-0 items-center justify-between gap-density-2">
                    <span className="min-w-0 truncate text-sm font-medium">
                      {prompt.name}
                    </span>
                    <span className="shrink-0 rounded border border-border px-1.5 py-0.5 text-[11px] uppercase text-muted-foreground">
                      {prompt.sourceKind}
                    </span>
                  </div>
                  <div className="mt-1 truncate text-xs text-muted-foreground">
                    {prompt.model || prompt.backend || "no model"}
                    {prompt.variables?.length
                      ? ` - ${prompt.variables.length} vars`
                      : ""}
                  </div>
                  <div className="mt-1 truncate text-xs text-muted-foreground">
                    {prompt.relPath}
                  </div>
                  {prompt.description && (
                    <div className="mt-1 line-clamp-2 text-xs text-muted-foreground">
                      {prompt.description}
                    </div>
                  )}
                  {prompt.parseError && (
                    <div className="mt-1 truncate text-xs text-destructive">
                      {prompt.parseError}
                    </div>
                  )}
                </button>
              );
            })}
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
  variables,
  variablesValid,
  onVariablesChange,
  onVariablesValidityChange,
  runtime,
  onRuntimeChange,
  models,
  tools,
  permissionCatalog,
  renderResult,
  activeRunID,
  onSelectRun,
  onRender,
  onRun,
  renderLoading,
  runLoading,
  renderEnabled,
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
  variables: Record<string, unknown>;
  variablesValid: boolean;
  onVariablesChange: (value: Record<string, unknown>) => void;
  onVariablesValidityChange: (valid: boolean) => void;
  runtime: AISpecRuntimeValue;
  onRuntimeChange: (value: AISpecRuntimeValue) => void;
  models: ChatModel[];
  tools: ToolMeta[];
  permissionCatalog?: AISpecRuntimePermissionCatalog;
  renderResult?: PromptRenderResult;
  activeRunID?: string;
  onSelectRun: (id: string | undefined) => void;
  onRender: () => void;
  onRun: () => void;
  renderLoading: boolean;
  runLoading: boolean;
  renderEnabled: boolean;
  runEnabled: boolean;
}) {
  const promptSchemaQuery = useQuery({
    queryKey: ["prompt-schema"],
    queryFn: fetchPromptSchema,
  });

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

  const schema = normalizeObjectSchema(detail.inputSchema);
  const backendCliArgs = promptSchemaQuery.data?.backends?.find(
    (backend) => backend.backend === runtime.backend,
  )?.args;

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="shrink-0 border-b border-border px-density-4 pt-density-3">
        <Tabs
          value={tab}
          onChange={onTabChange}
          tabs={[
            { id: "runner", label: "Run", icon: UiPlay },
            { id: "schema", label: "Schema", icon: UiListTree },
            { id: "source", label: "Source", icon: UiCode2 },
            { id: "runs", label: "Runs", icon: UiTerminal },
          ]}
        />
      </div>
      <div className="min-h-0 flex-1 overflow-auto p-density-4">
        {Boolean(error) && (
          <div className="mb-density-3 text-sm text-destructive">
            {errorMessage(error)}
          </div>
        )}
        {tab === "source" ? (
          <SourceEditor
            detail={detail}
            draft={draft}
            onDraftChange={onDraftChange}
          />
        ) : tab === "runs" ? (
          <RunningPrompts.RunsTab
            activeRunID={activeRunID}
            onSelectRun={onSelectRun}
          />
        ) : tab === "schema" ? (
          <SchemaPreview
            inputSchema={schema}
            outputSchema={normalizeObjectSchema(detail.outputSchema)}
          />
        ) : (
          <div className="grid min-h-full gap-density-4 xl:grid-cols-[minmax(340px,0.9fr)_minmax(0,1.1fr)]">
            <div className="space-y-density-4">
              <PromptRunEditor
                key={detail.id}
                value={runtime}
                onChange={onRuntimeChange}
                models={promptSelectableModels(models)}
                tools={tools}
                secretSelector={CAPTAIN_SECRET_SELECTOR}
                variables={variables}
                onVariablesChange={onVariablesChange}
                onVariablesValidityChange={onVariablesValidityChange}
                {...(permissionCatalog ? { permissionCatalog } : {})}
                {...(schema ? { variablesSchema: schema } : {})}
                {...(backendCliArgs
                  ? { cliOptions: { schema: backendCliArgs } }
                  : {})}
              />

              <div className="flex flex-wrap gap-density-2">
                <Button
                  size="sm"
                  variant="outline"
                  loading={renderLoading}
                  disabled={!renderEnabled || (!schema && !variablesValid)}
                  onClick={onRender}
                >
                  <Icon icon={UiCode2} className="size-4" />
                  Render
                </Button>
                <Button
                  size="sm"
                  loading={runLoading}
                  disabled={!runEnabled || (!schema && !variablesValid)}
                  onClick={onRun}
                >
                  <Icon icon={UiPlay} className="size-4" />
                  Run
                </Button>
              </div>
            </div>

            <RunnerOutput
              renderResult={renderResult}
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
  renderResult,
  activeRunID,
}: {
  renderResult?: PromptRenderResult;
  activeRunID?: string;
}) {
  if (activeRunID) {
    return <PromptRunStream runID={activeRunID} />;
  }
  if (renderResult) {
    return (
      <div className="space-y-density-3">
        {renderResult.validationError && (
          <div className="rounded-md border border-destructive/40 bg-destructive/10 p-density-3 text-sm text-destructive">
            {renderResult.validationError}
          </div>
        )}
        <CodeBlock language="markdown" source={renderResult.user || ""} />
        <CodeBlock
          language="json"
          source={JSON.stringify(renderResult.input ?? renderResult, null, 2)}
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

// SchemaPreview browses the prompt's input and output JSON schemas read-only,
// via clicky-ui's SchemaViewer. Absent schemas (most prompts declare no
// output.schema) degrade to an empty state rather than an error.
function SchemaPreview({
  inputSchema,
  outputSchema,
}: {
  inputSchema?: JsonSchemaObject;
  outputSchema?: JsonSchemaObject;
}) {
  return (
    <div className="grid min-h-full gap-density-4 xl:grid-cols-2">
      <SchemaPanel
        title="Input schema"
        icon={
          <Icon icon={UiFileSearch} className="size-4 text-muted-foreground" />
        }
        schema={inputSchema}
        emptyLabel="This prompt declares no input schema."
      />
      <SchemaPanel
        title="Output schema"
        icon={
          <Icon icon={UiListTree} className="size-4 text-muted-foreground" />
        }
        schema={outputSchema}
        emptyLabel="This prompt declares no output schema."
      />
    </div>
  );
}

function SchemaPanel({
  title,
  icon,
  schema,
  emptyLabel,
}: {
  title: string;
  icon: ReactNode;
  schema?: JsonSchemaObject;
  emptyLabel: string;
}) {
  return (
    <section className="space-y-density-2">
      <div className="flex items-center gap-density-2 text-sm font-semibold">
        {icon}
        {title}
      </div>
      {schema ? (
        <div className="rounded-md border border-border p-density-3">
          <SchemaViewer schema={schema} showControls defaultOpenDepth={1} />
        </div>
      ) : (
        <div className="flex min-h-[120px] items-center justify-center rounded-md border border-dashed border-border p-density-4 text-sm text-muted-foreground">
          {emptyLabel}
        </div>
      )}
    </section>
  );
}

function CreatePromptModal({
  open,
  onClose,
  sources,
  createOp,
  seedContent,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  sources: Array<{ id: string; label: string }>;
  createOp?: ResolvedOperation;
  seedContent?: string;
  onCreated: (prompt: PromptDetail) => void;
}) {
  const [name, setName] = useState("");
  const [relPath, setRelPath] = useState("");
  const [target, setTarget] = useState("");
  const [content, setContent] = useState(defaultPromptContent(""));
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | undefined>();

  useEffect(() => {
    if (!open) return;
    setName("");
    setRelPath("");
    setTarget(sources[0]?.id ?? "");
    setContent(seedContent || defaultPromptContent(""));
    setError(undefined);
  }, [open, seedContent, sources]);

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
      open={open}
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

function resolvePromptOps(operations: ResolvedOperation[]): PromptOps {
  return {
    list: findPromptOperation(operations, "list"),
    get: findPromptOperation(operations, "get"),
    create: findPromptOperation(operations, "create"),
    update: findPromptOperation(operations, "update"),
    delete: findPromptOperation(operations, "delete"),
    render: findPromptOperation(operations, "action", "render"),
    run: findPromptOperation(operations, "action", "run"),
  };
}

function findPromptOperation(
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

function requiredOperation(op: ResolvedOperation | undefined, name: string) {
  if (!op) throw new Error(`Prompt ${name} operation is not available.`);
  return op;
}

async function fetchPromptList(
  op: ResolvedOperation,
  params: { source: SourceFilter; query: string },
) {
  const response = await apiClient.executeCommand(
    op.path,
    op.method,
    { source: params.source, query: params.query },
    { Accept: "application/json" },
  );
  return unwrapResponse<PromptSummary[]>(response);
}

async function fetchChatModels() {
  const response = await fetch("/api/chat/models", {
    headers: { Accept: "application/json" },
  });
  if (!response.ok) {
    const message = await response.text();
    throw new Error(message || `Model catalog failed with ${response.status}`);
  }
  return (await response.json()) as ChatModel[];
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

function unwrapResponse<T>(response: ExecutionResponse): T {
  if (!response.success) {
    throw new Error(response.error || response.output || "Operation failed.");
  }
  return response.parsed as T;
}

function resolveOperationPath(path: string, params: Record<string, string>) {
  let next = path;
  for (const [key, value] of Object.entries(params)) {
    next = next.replace(`{${key}}`, encodeURIComponent(value));
  }
  return next;
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
  );
}

function normalizeSpecRuntimePayload(
  payload: Record<string, unknown>,
  models: ChatModel[],
) {
  const spec = payload.spec;
  if (!spec || typeof spec !== "object" || Array.isArray(spec)) return payload;
  const specRecord = { ...(spec as Record<string, unknown>) };
  if (typeof specRecord.model === "string") {
    const selected = normalizeRuntimeModel(specRecord.model, models);
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

// Seeds the runtime spec's backend from the prompt (explicit, else inferred from
// the model). The PromptRunEditor derives the family/mode picker from spec.backend.
function runtimeSelectionFromPrompt(prompt: PromptSummary): AISpecRuntimeValue {
  const backend =
    prompt.backend?.trim() || inferBackendFromModel(prompt.model || "");
  return backend ? { backend } : {};
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
  if (value.startsWith("codex")) return "codex-cli";
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

function normalizeRuntimeModel(model: string, models: ChatModel[]) {
  const id = model.trim();
  if (!id) return { model: "", backend: "" };

  const selected = models.find((entry) => entry.id === id);
  if (!selected) return { model: id, backend: "" };

  const backend = providerToBackend(selected.provider);
  if (
    selected.provider === "anthropic" ||
    selected.provider === "openai" ||
    selected.provider === "googleai" ||
    selected.provider === "deepseek"
  ) {
    return { model: stripProviderPrefix(id), backend };
  }
  if (selected.provider === "codex-cli" && id.startsWith("codex-")) {
    return { model: id.slice("codex-".length), backend };
  }
  return { model: id, backend };
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

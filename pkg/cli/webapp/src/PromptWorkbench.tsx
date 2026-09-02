import { useEffect, useMemo, useReducer, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  AppShell,
  type AppShellProps,
  type AppShellNavSection,
} from "@flanksource/clicky-ui/components";
import { type ChatModel } from "@flanksource/clicky-ui/chat";
import { useOperations } from "@flanksource/clicky-ui/rpc";
import { apiClient } from "./api";
import { isReadOnlyDbContext } from "./dbContext";
import {
  isPromptBatchHandle,
  type PromptExecutionHandle,
} from "./hooks/usePromptRunStream";
import { CAPTAIN_SIDEBAR_COLLAPSE_KEY } from "./shellHelpers";
import {
  fetchPromptList,
  requiredOperation,
  resolvePromptOps,
  type PromptDetail,
  type PromptPreviewResult,
  type PromptSourceFilter,
  type PromptSummary,
} from "./promptData";
import { resolveProvider } from "./promptWorkbenchHelpers";
import {
  PromptWriteModal,
  promptWriteSeed,
  type PromptWriteInput,
  type PromptWriteMode,
  type PromptWriteSource,
} from "./PromptWriteModal";
import { PromptDeleteDialog } from "./PromptDeleteDialog";
import { PromptWorkbenchActions } from "./PromptWorkbenchActions";
import { PromptHeader, PromptSidebar } from "./PromptSidebar";
import { PromptDetailPane, type DetailTab } from "./PromptDetailPane";
import { PromptCatalogView } from "./PromptCatalogView";
import {
  SCRATCH_PROMPT,
  anyPromptDirty,
  isPromptDirty,
  isScratchPrompt,
  promptDetailReducer,
  promptDetailStateFor,
  type PromptDetailStates,
} from "./promptDetailState";
import {
  errorMessage,
  executePromptOperation,
  fetchPermissionCatalog,
  fetchPromptDetail,
  fetchPromptSchema,
  promptActionParams,
  submitPromptOperation,
} from "./promptWorkbenchApi";
import { AGENT_TOOLS } from "./promptAgentTools";
import { usePromptRuntimeProfiles } from "./runtimeProfilesData";
import { useWhoamiCatalog } from "./whoamiCatalog";

type Navigate = (to: string, opts?: { replace?: boolean }) => void;

type PromptWorkbenchProps = {
  selectedId?: string;
  onNavigate: Navigate;
  navSections: AppShellNavSection[];
  actions: AppShellProps["actions"];
  search: AppShellProps["search"];
};

const EMPTY_PROMPTS: PromptSummary[] = [];
const EMPTY_MODELS: ChatModel[] = [];
const NO_DETAIL_STATES: PromptDetailStates = {};

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
  const [source, setSource] = useState<PromptSourceFilter>("all");
  const [query, setQuery] = useState("");
  const [tab, setTab] = useState<DetailTab>("runner");
  const [detailStates, dispatchDetailState] = useReducer(
    promptDetailReducer,
    NO_DETAIL_STATES,
  );
  const [writeMode, setWriteMode] = useState<PromptWriteMode>();
  const [deleteOpen, setDeleteOpen] = useState(false);

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
  const whoamiCatalogQuery = useWhoamiCatalog();
  const runtimeProfiles = usePromptRuntimeProfiles();
  const prompts = listQuery.data ?? EMPTY_PROMPTS;
  const models = whoamiCatalogQuery.data?.models ?? EMPTY_MODELS;
  const runtimeCatalog = whoamiCatalogQuery.data?.runtimes;
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
  const selectedDetailState = promptDetailStateFor(detailStates, detail);
  // The permission catalog describes one agent's world, so both halves of the
  // selected runtime identify it and trigger a refetch when either changes.
  const selectedProvider = resolveProvider(
    models,
    selectedDetailState.runRequest.spec?.model,
  );
  const selectedMode = selectedDetailState.runRequest.spec?.mode;
  const selectedRuntime =
    selectedProvider && selectedMode
      ? { provider: selectedProvider, mode: selectedMode }
      : undefined;
  const permissionCatalogQuery = useQuery({
    queryKey: ["permission-catalog", selectedProvider, selectedMode],
    queryFn: () => fetchPermissionCatalog(selectedRuntime!),
    enabled: Boolean(selectedRuntime),
  });
  const scratch = isScratchPrompt(detail);
  const dirty = Boolean(detail) && isPromptDirty(selectedDetailState, detail);
  const repair = Boolean(detail?.parseError);
  const writableSources = useMemo<PromptWriteSource[]>(
    () =>
      (promptSchemaQuery.data?.sources ?? [])
        .filter((item) => item.writable)
        .map((item) => ({
          id: item.id,
          label: item.label,
          ...(item.root ? { root: item.root } : {}),
        })),
    [promptSchemaQuery.data?.sources],
  );
  const canSave = Boolean(
    detail &&
    !scratch &&
    detail.writable &&
    promptOps.update &&
    selectedDetailState.schemaValidity.input &&
    selectedDetailState.schemaValidity.output &&
    dirty,
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

  // Drafts survive in-app navigation (each prompt keeps its slot); only a page
  // unload can still lose them, so that is the one place the browser asks.
  const unsavedAnywhere = anyPromptDirty(detailStates);
  useEffect(() => {
    if (!unsavedAnywhere) return;
    const warn = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [unsavedAnywhere]);

  async function refreshAll() {
    await listQuery.refetch();
    if (activePromptId) await detailQuery.refetch();
  }

  function selectRun(id: string | undefined) {
    dispatchDetailState({ type: "active-batch", detail, value: undefined });
    dispatchDetailState({ type: "active-run", detail, value: id });
    if (id) setTab("runner");
  }

  // Render and Run act on the unsaved draft when there is one, so what the user
  // sees in the editor is what executes.
  function actionBody(): Record<string, unknown> {
    return {
      ...selectedDetailState.runRequest,
      ...(dirty ? { content: selectedDetailState.draft } : {}),
    };
  }

  async function saveDraft() {
    if (!detail?.writable || scratch || !promptOps.update) return;
    dispatchDetailState({ type: "action-error", detail, value: undefined });
    dispatchDetailState({ type: "action-loading", detail, value: "save" });
    try {
      const saved = await submitPromptOperation<PromptDetail>(
        promptOps.update,
        { id: detail.id },
        {
          content: selectedDetailState.draft,
          ...(detail.version ? { baseVersion: detail.version } : {}),
        },
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
        actionBody(),
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
        actionBody(),
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
      setDeleteOpen(false);
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
            dirty={dirty}
          />
        }
        bodyActions={
          <PromptWorkbenchActions
            detail={detail}
            scratch={scratch}
            canCreate={Boolean(promptOps.create)}
            canUpdate={Boolean(promptOps.update)}
            canDelete={Boolean(promptOps.delete)}
            canSave={canSave}
            canSaveAs={canSaveAs}
            saving={selectedDetailState.actionLoading === "save"}
            onSelectRun={selectRun}
            onDuplicate={() => setWriteMode("duplicate")}
            onDelete={() => setDeleteOpen(true)}
            onSave={() => void saveDraft()}
            onSaveAs={() => setWriteMode("save-as")}
            onRefresh={() => void refreshAll()}
          />
        }
        bodySplit={30}
        contentClassName="p-0 overflow-hidden"
      >
        <PromptDetailPane
          detail={detail}
          hasSelection={hasSelection}
          emptyState={
            <PromptCatalogView
              prompts={prompts}
              loading={listQuery.isLoading || operationsLoading}
              error={listQuery.error ?? operationsError}
              onSelect={(id) => onNavigate(`/prompts/${encodeURIComponent(id)}`)}
            />
          }
          loading={Boolean(activePromptId && detailQuery.isLoading)}
          error={
            detailQuery.error ??
            promptSchemaQuery.error ??
            whoamiCatalogQuery.error ??
            selectedDetailState.actionError ??
            runtimeProfiles.error
          }
          tab={tab}
          onTabChange={(next) => setTab(next as DetailTab)}
          draft={selectedDetailState.draft}
          draftDirty={dirty}
          onDraftChange={(value) => dispatchDetailState({ type: "draft", detail, value })}
          onSchemaValidityChange={(kind, value) =>
            dispatchDetailState({ type: "schema-validity", detail, kind, value })
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
          {...runtimeProfiles.editorProps}
          previewResult={selectedDetailState.previewResult}
          activeRunID={selectedDetailState.activeRunID}
          activeBatch={selectedDetailState.activeBatch}
          onEditBatch={() => dispatchDetailState({ type: "active-batch", detail, value: undefined })}
          onSelectRun={selectRun}
          onPreview={() => void previewPrompt()}
          onRun={() => void runPrompt()}
          previewLoading={selectedDetailState.actionLoading === "preview"}
          runLoading={selectedDetailState.actionLoading === "run"}
          previewEnabled={Boolean(promptOps.preview && detail) && !repair}
          // Running a prompt writes its session; a read-only database context
          // would have the POST rejected, so the control is disabled instead.
          runEnabled={
            Boolean(promptOps.run && detail) && !repair && !isReadOnlyDbContext()
          }
        />
      </AppShell>
      <PromptWriteModal
        open={writeMode !== undefined}
        mode={writeMode ?? "create"}
        onClose={() => setWriteMode(undefined)}
        sources={writableSources}
        onSubmit={createPromptCopy}
        {...promptWriteSeed(
          writeMode,
          scratch ? undefined : detail,
          selectedDetailState.draft,
        )}
      />
      {detail && !scratch && (
        <PromptDeleteDialog
          prompt={detail}
          open={deleteOpen}
          loading={selectedDetailState.actionLoading === "delete"}
          onConfirm={() => void deletePrompt()}
          onClose={() => setDeleteOpen(false)}
        />
      )}
    </>
  );
}

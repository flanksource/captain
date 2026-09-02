import type { ReactNode } from "react";
import { Button, Tabs } from "@flanksource/clicky-ui/components";
import {
  CodeBlock,
  Icon,
  UiCode2,
  UiListTree,
  UiPlay,
  UiTerminal,
} from "@flanksource/clicky-ui/data";
import {
  PromptRunEditor,
  familiesFromRuntimeCatalog,
  type AIPromptRunValue,
  type AISpecRuntimePermissionCatalog,
  type ResolvedRuntimeProfile,
  type RuntimeCatalogFamily,
  type RuntimePreset,
  type RuntimeProfile,
  type RuntimeProfileResolveRequest,
  type ToolMeta,
} from "@flanksource/clicky-ui/ai";
import { type ChatModel } from "@flanksource/clicky-ui/chat";
import { resolveProvider } from "./promptWorkbenchHelpers";
import { PromptRunStream } from "./PromptRunStream";
import { PromptBatchInspector } from "./PromptBatchInspector";
import { PromptSchemaEditor } from "./PromptSchemaEditor";
import { RunningPromptsRunsTab } from "./RunningPrompts";
import { PromptSourceMarkdownEditor } from "./PromptWriteModal";
import type { PromptBatchHandle } from "./hooks/usePromptRunStream";
import type { PromptDetail, PromptPreviewResult } from "./promptData";
import type { PromptSchemaKind } from "./promptSchemaSource";
import { isScratchPrompt } from "./promptDetailState";
import {
  CAPTAIN_SECRET_SELECTOR,
  errorMessage,
  normalizeObjectSchema,
  type PromptSchemaDoc,
} from "./promptWorkbenchApi";

export type DetailTab = "source" | "runner" | "schema" | "runs";

const RUN_TAB = { id: "runner", label: "Run", icon: UiPlay };
const SCHEMA_TAB = { id: "schema", label: "Schema", icon: UiListTree };
const SOURCE_TAB = { id: "source", label: "Source", icon: UiCode2 };
const RUNS_TAB = { id: "runs", label: "Runs", icon: UiTerminal };

export function PromptDetailPane({
  detail,
  hasSelection,
  emptyState,
  loading,
  error,
  tab,
  onTabChange,
  draft,
  draftDirty,
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
  presets,
  profiles,
  onSaveProfile,
  onCreateProfile,
  onResolveProfile,
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
  emptyState?: ReactNode;
  loading: boolean;
  error: unknown;
  tab: DetailTab;
  onTabChange: (tab: string) => void;
  draft: string;
  draftDirty: boolean;
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
  /** Saved runtime presets and profiles; both present enables the profile picker in the spec editor. */
  presets?: RuntimePreset[];
  profiles?: RuntimeProfile[];
  onSaveProfile?: (profile: RuntimeProfile) => Promise<RuntimeProfile>;
  onCreateProfile?: (profile: RuntimeProfile) => Promise<RuntimeProfile>;
  onResolveProfile?: (request: RuntimeProfileResolveRequest) => Promise<ResolvedRuntimeProfile>;
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
      emptyState ?? (
        <div className="flex h-full items-center justify-center p-6 text-sm text-muted-foreground">
          Select a prompt.
        </div>
      )
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
  // A prompt whose frontmatter no longer parses opens in repair mode: only the
  // raw source is editable, and nothing can be rendered or run until it parses.
  const repair = Boolean(detail.parseError);
  const tabs = scratch
    ? [RUN_TAB, RUNS_TAB]
    : repair
      ? [SOURCE_TAB]
      : [RUN_TAB, SCHEMA_TAB, SOURCE_TAB, RUNS_TAB];
  const activeTab = repair
    ? "source"
    : scratch && (tab === "source" || tab === "schema")
      ? "runner"
      : tab;
  const schema = scratch
    ? undefined
    : normalizeObjectSchema(detail.inputSchema);
  const selectedProvider = resolveProvider(
    models,
    runRequest.spec?.model,
  );
  const runtimeCliArgs = promptSchema?.runtimeAdapters?.find(
    (runtime) =>
      runtime.provider === selectedProvider &&
      runtime.mode === runRequest.spec?.mode,
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
        {repair && (
          <div
            role="alert"
            className="mb-density-3 rounded-md border border-destructive/40 bg-destructive/10 p-density-3 text-sm"
          >
            <div className="font-medium text-destructive">
              This prompt no longer parses. Fix the source and save it; Render
              and Run are unavailable until it parses.
            </div>
            <pre className="mt-density-2 whitespace-pre-wrap font-mono text-xs text-destructive">
              {detail.parseError}
            </pre>
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
                {...(runtimeCliArgs
                  ? { cliOptions: { schema: runtimeCliArgs } }
                  : {})}
                {...(promptSchema?.sandboxes
                  ? { sandboxCatalog: promptSchema.sandboxes }
                  : {})}
                {...(profiles && presets ? { profiles, presets } : {})}
                {...(onSaveProfile ? { onSaveProfile } : {})}
                {...(onCreateProfile ? { onCreateProfile } : {})}
                {...(onResolveProfile ? { onResolveProfile } : {})}
                {...(previewResult?.resolution
                  ? { resolution: previewResult.resolution }
                  : {})}
                {...(scratch
                  ? {
                      promptLabel: "Scratch prompt",
                      promptPlaceholder: "Write a one-off prompt",
                    }
                  : {})}
              />

              <div className="flex flex-wrap items-center gap-density-2">
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
                {draftDirty && (
                  <span
                    className="rounded border border-amber-500/50 px-1.5 py-0.5 text-[11px] uppercase text-amber-600 dark:text-amber-400"
                    title="Render and Run use the unsaved source, not the file on disk"
                  >
                    unsaved draft — render and run use it
                  </span>
                )}
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

import { useState, type ReactNode } from "react";
import { Button, Modal } from "@flanksource/clicky-ui/components";
import { Icon, UiAdd, UiSave } from "@flanksource/clicky-ui/data";
import "@flanksource/clicky-ui/mdx-editor.css";
import { MdxEditorField } from "@flanksource/clicky-ui/mdx-editor";
import { promptWriteDestination } from "./promptWriteDestination";

export type PromptWriteInput = {
  target: string;
  name: string;
  relPath: string;
  content: string;
};

/** create: a fresh prompt; save-as: fork a read-only prompt; duplicate: copy an existing one. */
export type PromptWriteMode = "create" | "save-as" | "duplicate";

export type PromptWriteSource = {
  id: string;
  label: string;
  /** Directory the file lands in; shown in the destination preview. */
  root?: string;
};

const MODAL_TITLES: Record<PromptWriteMode, string> = {
  create: "New Prompt",
  "save-as": "Save Prompt As",
  duplicate: "Duplicate Prompt",
};

/**
 * What the modal starts from: save-as and duplicate carry the current draft,
 * create starts empty — a new prompt is never silently seeded from whatever
 * happened to be selected.
 */
export function promptWriteSeed(
  mode: PromptWriteMode | undefined,
  from: { name: string } | undefined,
  draft: string,
): Pick<PromptWriteModalProps, "initialName" | "initialContent"> {
  if (!from || mode === "create" || mode === undefined) return {};
  return {
    initialName: mode === "duplicate" ? `${from.name} copy` : from.name,
    initialContent: draft,
  };
}

export type PromptWriteModalProps = {
  open: boolean;
  mode: PromptWriteMode;
  sources: PromptWriteSource[];
  initialName?: string;
  initialContent?: string;
  onClose: () => void;
  onSubmit: (input: PromptWriteInput) => Promise<void>;
};

export function PromptWriteAction({
  writable,
  disabled,
  loading,
  onClick,
}: {
  writable: boolean;
  disabled: boolean;
  loading: boolean;
  onClick: () => void;
}) {
  return (
    <Button
      size="sm"
      variant="outline"
      disabled={disabled}
      loading={loading}
      onClick={onClick}
    >
      <Icon icon={UiSave} className="size-4" />
      {writable ? "Save" : "Save as…"}
    </Button>
  );
}

export function PromptWriteModal({
  open,
  ...props
}: PromptWriteModalProps) {
  if (!open) return null;
  return <PromptWriteModalForm {...props} />;
}

function PromptWriteModalForm({
  mode,
  sources,
  initialName,
  initialContent,
  onClose,
  onSubmit,
}: Omit<PromptWriteModalProps, "open">) {
  const seeded = initialContent !== undefined;
  const [name, setName] = useState(initialName ?? "");
  const [relPath, setRelPath] = useState("");
  const [target, setTarget] = useState(() => sources[0]?.id ?? "");
  const [content, setContent] = useState(
    () => initialContent ?? defaultPromptContent(""),
  );
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string>();
  const saveAs = mode === "save-as";
  const destination = promptWriteDestination(
    sources.find((source) => source.id === target),
    relPath,
    name,
  );

  async function submit() {
    setLoading(true);
    setError(undefined);
    try {
      await onSubmit({ target, name, relPath, content });
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  return (
    <Modal
      open
      onClose={onClose}
      title={MODAL_TITLES[mode]}
      size="xl"
      footer={
        <div className="flex justify-end gap-density-2">
          <Button size="sm" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            size="sm"
            loading={loading}
            disabled={
              (!name.trim() && !relPath.trim()) || !content.trim()
            }
            onClick={() => void submit()}
          >
            <Icon icon={saveAs ? UiSave : UiAdd} className="size-4" />
            {saveAs ? "Save As" : "Create"}
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
                if (mode === "create" && !seeded && !relPath) {
                  setContent(defaultPromptContent(next));
                }
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
        <div className="text-xs text-muted-foreground" data-testid="prompt-write-destination">
          {destination ? (
            <>
              Writes <code className="font-mono">{destination}</code>
            </>
          ) : (
            "Enter a name or a path to see where the file will be written."
          )}
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

export function PromptSourceMarkdownEditor({
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
    <label className="block space-y-1 text-xs text-muted-foreground">
      <span>{label}</span>
      {children}
    </label>
  );
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

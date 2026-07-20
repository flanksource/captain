import { lazy, Suspense, useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@flanksource/clicky-ui/components";
import {
  parseSchemaEditorValue,
  readPromptSchemas,
  updatePromptSchemaSource,
  type PromptSchemaKind,
} from "./promptSchemaSource";

const MonacoEditor = lazy(() =>
  import("@flanksource/clicky-ui/monaco").then((module) => ({
    default: module.MonacoEditor,
  })),
);

type PromptSchemaEditorProps = {
  promptId: string;
  source: string;
  onSourceChange: (source: string) => void;
  onValidityChange: (kind: PromptSchemaKind, valid: boolean) => void;
};

export function PromptSchemaEditor({
  promptId,
  source,
  onSourceChange,
  onValidityChange,
}: PromptSchemaEditorProps) {
  const parsed = useMemo(() => {
    try {
      return { schemas: readPromptSchemas(source), error: "" };
    } catch (cause) {
      return {
        schemas: { input: undefined, output: undefined },
        error: cause instanceof Error ? cause.message : String(cause),
      };
    }
  }, [source]);
  const validityChangeRef = useRef(onValidityChange);
  validityChangeRef.current = onValidityChange;
  useEffect(() => {
    const valid = !parsed.error;
    validityChangeRef.current("input", valid);
    validityChangeRef.current("output", valid);
  }, [parsed.error]);

  if (parsed.error) {
    return (
      <div className="text-sm text-destructive" role="alert">
        {parsed.error}
      </div>
    );
  }

  return (
    <div className="grid min-h-full gap-density-4 xl:grid-cols-2">
      <SchemaPanelEditor
        title="Input schema"
        kind="input"
        promptId={promptId}
        source={source}
        schema={parsed.schemas.input}
        onSourceChange={onSourceChange}
        onValidityChange={onValidityChange}
      />
      <SchemaPanelEditor
        title="Output schema"
        kind="output"
        promptId={promptId}
        source={source}
        schema={parsed.schemas.output}
        onSourceChange={onSourceChange}
        onValidityChange={onValidityChange}
      />
    </div>
  );
}

function SchemaPanelEditor({
  title,
  kind,
  promptId,
  source,
  schema,
  onSourceChange,
  onValidityChange,
}: {
  title: string;
  kind: PromptSchemaKind;
  promptId: string;
  source: string;
  schema?: Record<string, unknown>;
  onSourceChange: (source: string) => void;
  onValidityChange: (kind: PromptSchemaKind, valid: boolean) => void;
}) {
  const [value, setValue] = useState(() => formatSchema(schema));
  const [present, setPresent] = useState(Boolean(schema));
  const [error, setError] = useState("");
  const modelRef = useRef<{ dispose: () => void } | null>(null);
  useEffect(
    () => () => {
      modelRef.current?.dispose();
      modelRef.current = null;
    },
    [],
  );

  function change(next: string) {
    setValue(next);
    try {
      const parsed = parseSchemaEditorValue(next);
      onSourceChange(updatePromptSchemaSource(source, kind, parsed));
      setPresent(true);
      setError("");
      onValidityChange(kind, true);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
      onValidityChange(kind, false);
    }
  }

  function remove() {
    try {
      onSourceChange(updatePromptSchemaSource(source, kind, undefined));
      setValue(formatSchema(undefined));
      setPresent(false);
      setError("");
      onValidityChange(kind, true);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
      onValidityChange(kind, false);
    }
  }

  return (
    <section className="space-y-density-2">
      <div className="flex items-center justify-between gap-density-2">
        <div className="text-sm font-semibold">{title}</div>
        {present && (
          <Button type="button" size="sm" variant="ghost" onClick={remove}>
            Remove
          </Button>
        )}
      </div>
      <Suspense
        fallback={
          <div className="h-80 rounded-md border border-input p-density-3 text-sm text-muted-foreground">
            Loading schema editor…
          </div>
        }
      >
        <MonacoEditor
          language="json"
          value={value}
          onChange={change}
          path={`file:///captain-prompt-${encodeURIComponent(promptId)}-${kind}-schema.json`}
          height="calc(100vh - 18rem)"
          onMount={(editor) => {
            modelRef.current = editor.getModel();
          }}
        />
      </Suspense>
      {error && (
        <div className="text-sm text-destructive" role="alert">
          {error}
        </div>
      )}
      {!present && !error && (
        <div className="text-xs text-muted-foreground">
          This prompt declares no {kind} schema. Editing this object adds one.
        </div>
      )}
    </section>
  );
}

function formatSchema(schema: Record<string, unknown> | undefined) {
  return JSON.stringify(
    schema ?? { type: "object", properties: {} },
    null,
    2,
  );
}

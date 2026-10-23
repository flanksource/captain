import { useEffect, useId, useRef, useState } from "react";
import { Button, Field, InputField, JsonSchemaForm, type JsonSchemaObject } from "@flanksource/clicky-ui/components";
import { createSchemaDraft, editSchemaDraft, type SchemaDraft } from "./adapterSchemaDraft";
import { validateAdapterSchema, type AdapterSchemaDocument, type SchemaValidation } from "./adapterSchemas";

export function AdapterSchemaPreview({ document, view, draft, onChange }: {
  document: AdapterSchemaDocument; view: string; draft: SchemaDraft | undefined; onChange: (draft: SchemaDraft) => void;
}) {
  const id = useId();
  const current = draft ?? createSchemaDraft({});
  const [result, setResult] = useState<SchemaValidation>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const request = useRef<AbortController | undefined>(undefined);
  useEffect(() => () => request.current?.abort(), []);
  const change = (next: SchemaDraft) => {
    request.current?.abort();
    setResult(undefined); setError(""); setLoading(false); onChange(next);
  };
  const wrapped = !document.schema.properties || Boolean(document.schema.additionalProperties);
  const formSchema: JsonSchemaObject = wrapped ? {
    type: "object", properties: { value: document.schema },
    ...(document.schema.$defs ? { $defs: document.schema.$defs } : {}),
    ...(document.schema.definitions ? { definitions: document.schema.definitions } : {}),
  } : document.schema;
  const object = current.value !== null && typeof current.value === "object" && !Array.isArray(current.value);
  return <div className="min-w-0 space-y-density-4">
    <p className="text-xs text-muted-foreground">Local preview. Values stay in this page and are never saved or applied to a runtime.</p>
    {view === "form" ? <>
      {current.error && <p role="alert" className="text-sm text-destructive">Correct the JSON or YAML draft before editing the form: {current.error}</p>}
      {!wrapped && !object ? <p role="alert">This resource requires an object. Correct its JSON or YAML value.</p> :
        <JsonSchemaForm schema={formSchema} value={wrapped ? { value: current.value } : current.value as Record<string, unknown>}
          onChange={(value) => change(createSchemaDraft(wrapped ? value.value : value))} readOnly={Boolean(current.error)}
          idPrefix={id} layout={{ mode: "stacked", help: "hover" }} size="sm" applyDefaults={false}
          {...(result ? { errors: result.errors.map((issue) => ({ ...issue, instancePath: wrapped ? `/value${issue.instancePath}` : issue.instancePath })) } : {})} />}
    </> : <Field label={`${view === "yaml" ? "YAML" : "JSON"} value`} htmlFor={`${id}-source`}>
      <InputField as="textarea" id={`${id}-source`} value={view === "yaml" ? current.yaml : current.json}
        inputClassName="min-h-[28rem] font-mono text-xs" spellCheck={false}
        onChange={(text) => change(editSchemaDraft(current, view === "yaml" ? "yaml" : "json", text))} />
    </Field>}
    {current.error && view !== "form" && <p role="alert" className="whitespace-pre-wrap text-sm text-destructive">{current.error}</p>}
    <div className="flex flex-wrap items-center gap-density-2">
      <Button size="sm" variant="outline" disabled={Boolean(current.error)} loading={loading} onClick={async () => {
        request.current?.abort();
        const controller = new AbortController(); request.current = controller;
        setLoading(true); setError(""); setResult(undefined);
        try { const next = await validateAdapterSchema(document.id, current.value, controller.signal); if (!controller.signal.aborted) setResult(next); }
        catch (failure) { if (!controller.signal.aborted) setError(failure instanceof Error ? failure.message : String(failure)); }
        finally { if (!controller.signal.aborted) setLoading(false); }
      }}>Validate schema</Button>
      {result && <span role="status" className={result.valid ? "text-sm text-primary" : "text-sm text-destructive"}>{result.valid ? "Schema valid" : `${result.errors.length} validation error${result.errors.length === 1 ? "" : "s"}`}</span>}
    </div>
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    {result?.errors.map((issue, index) => <p key={`${issue.instancePath}:${index}`} className="break-words text-xs text-destructive">{issue.instancePath || "/"}: {issue.message}</p>)}
  </div>;
}

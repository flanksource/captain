import { useId, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Field, InputField, Panel, SegmentedControl, Select } from "@flanksource/clicky-ui/components";
import { SchemaViewer } from "@flanksource/clicky-ui/data";
import { StateMessage } from "./StateMessage";
import { AdapterSchemaPreview } from "./AdapterSchemaPreview";
import { AdapterSchemaProvenance } from "./AdapterSchemaProvenance";
import { adapterSchemaLocation, fetchAdapterSchemas, selectAdapterSchema, type AdapterSchemaDocument, type AdapterSchemaSelection } from "./adapterSchemas";
import type { SchemaDraft } from "./adapterSchemaDraft";

type Navigate = (to: string, opts?: { replace?: boolean }) => void;

const PROVIDER_LABELS: Record<string, string> = {
  anthropic: "Anthropic", openai: "OpenAI", google: "Google", deepseek: "DeepSeek", jev: "Jev", captain: "Captain",
};

export function AdapterSchemasPage({ selection, search = "", onNavigate }: { selection: AdapterSchemaSelection; search?: string; onNavigate: Navigate }) {
  const query = useQuery({ queryKey: ["adapter-schemas"], queryFn: fetchAdapterSchemas, staleTime: Number.POSITIVE_INFINITY });
  const selected = selectAdapterSchema(query.data ?? [], selection);
  return <div className="h-full overflow-auto">
    <div className="mx-auto flex w-full max-w-[110rem] flex-col gap-density-4 p-density-4 md:p-density-6">
      <header className="space-y-density-2">
        <h1 className="text-2xl font-semibold tracking-tight">Adapter schemas</h1>
        <p className="text-sm text-muted-foreground">Explore native provider resources and portable Captain configuration. Try form inputs locally and inspect their JSON or YAML.</p>
      </header>
      {query.isLoading ? <StateMessage>Loading schemas...</StateMessage>
        : query.error ? <StateMessage tone="error">{errorMessage(query.error)}</StateMessage>
        : selected && query.data ? <AdapterSchemaWorkspace documents={query.data} selected={selected} locationSearch={search} onNavigate={onNavigate} />
        : <StateMessage tone="error">{query.data?.length ? `Unknown schema: ${selection.provider}/${selection.mode}` : "The schema catalog returned no schemas."}</StateMessage>}
    </div>
  </div>;
}

function AdapterSchemaWorkspace({ documents, selected, locationSearch, onNavigate }: {
  documents: AdapterSchemaDocument[]; selected: AdapterSchemaDocument; locationSearch: string; onNavigate: Navigate;
}) {
  const id = useId();
  const parameters = new URLSearchParams(locationSearch);
  const search = parameters.get("q") ?? "";
  const view = parameters.get("view") ?? "schema";
  if (!["schema", "form", "json", "yaml"].includes(view)) throw new Error(`Unknown schema view: ${view}`);
  const [drafts, setDrafts] = useState<Record<string, SchemaDraft>>({});
  const providers = useMemo(() => [...new Set(documents.map((document) => document.provider))], [documents]);
  const resources = documents.filter((document) => document.provider === selected.provider);
  const matches = resources.filter((document) => `${document.id} ${document.title} ${document.description}`.toLowerCase().includes(search.toLowerCase()));
  const location = (document: AdapterSchemaDocument) => adapterSchemaLocation({ provider: document.provider, mode: document.id.slice(document.provider.length + 1) }) + (parameters.size ? `?${parameters}` : "");
  const navigate = (document: AdapterSchemaDocument) => onNavigate(location(document));
  const updateQuery = (key: string, value: string) => {
    if (value) parameters.set(key, value); else parameters.delete(key);
    onNavigate(location(selected), { replace: true });
  };
  const label = selected.kind === "execution" ? `${PROVIDER_LABELS[selected.provider] ?? selected.provider} ${selected.mode === "api" ? "API" : selected.mode === "cli" ? "CLI" : "Agent"}` : selected.title;
  return <div className="grid min-h-0 gap-density-4 lg:grid-cols-[18rem_minmax(0,1fr)]">
    <Panel title="Resources">
      <div className="space-y-density-4">
        <Field label="Provider" htmlFor={`${id}-provider`}>
          <Select id={`${id}-provider`} value={selected.provider} options={providers.map((provider) => ({ value: provider, label: PROVIDER_LABELS[provider] ?? provider }))}
            onChange={(event) => {
              const first = documents.find((document) => document.provider === event.target.value);
              if (!first) throw new Error(`Provider ${event.target.value} has no schemas`);
              navigate(first);
            }} />
        </Field>
        <Field label="Search resources" htmlFor={`${id}-search`}><InputField id={`${id}-search`} type="search" value={search} onChange={(value) => updateQuery("q", value)} /></Field>
        <Field label="Resource" htmlFor={`${id}-resource`}>
          <Select id={`${id}-resource`} value={selected.id} options={resources.map((document) => ({ value: document.id, label: document.title }))}
            onChange={(event) => {
              const document = resources.find((resource) => resource.id === event.target.value);
              if (!document) throw new Error(`Unknown resource ${event.target.value}`);
              navigate(document);
            }} />
        </Field>
        <div className="max-h-28 space-y-1 overflow-auto lg:max-h-80" aria-label="Matching resources">
          {matches.map((document) => <a key={document.id} href={location(document)}
            aria-current={document.id === selected.id ? "page" : undefined}
            onClick={(event) => { if (!event.metaKey && !event.ctrlKey) { event.preventDefault(); navigate(document); } }}
            className={`block rounded px-density-2 py-density-1 text-sm ${document.id === selected.id ? "bg-primary/10 font-medium text-primary" : "text-muted-foreground hover:bg-muted"}`}>
            {document.title}<span className="block font-mono text-xs">{document.id}</span>
          </a>)}
          {matches.length === 0 && <p className="text-sm text-muted-foreground">No matching resources.</p>}
        </div>
        <p className="text-xs text-muted-foreground">{documents.length} schemas across {providers.length} providers</p>
      </div>
    </Panel>
    <Panel title={label}>
      <div className="min-w-0 space-y-density-4">
        <div className="space-y-density-1"><h2 className="text-lg font-semibold">{label}</h2><p className="text-sm text-muted-foreground">{selected.description}</p><p className="font-mono text-xs text-muted-foreground">{selected.id} · {selected.kind}</p></div>
        <AdapterSchemaProvenance document={selected} />
        <SegmentedControl aria-label="View" value={view} options={[{ id: "schema", label: "Schema" }, { id: "form", label: "Form" }, { id: "json", label: "JSON" }, { id: "yaml", label: "YAML" }]}
          onChange={(value) => updateQuery("view", value)} wrap />
        {view === "schema" ? <SchemaViewer key={selected.id} schema={selected.schema} showControls defaultOpenDepth={1} className="min-h-[32rem]" />
          : <AdapterSchemaPreview key={selected.id} document={selected} view={view} draft={drafts[selected.id]}
              onChange={(draft) => setDrafts((previous) => ({ ...previous, [selected.id]: draft }))} />}
      </div>
    </Panel>
  </div>;
}

function errorMessage(error: unknown): string { return error instanceof Error ? error.message : String(error); }

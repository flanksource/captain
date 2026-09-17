import { useId, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Field,
  Panel,
  SegmentedControl,
  Select,
} from "@flanksource/clicky-ui/components";
import { SchemaViewer } from "@flanksource/clicky-ui/data";
import { StateMessage } from "./StateMessage";
import {
  adapterSchemaLocation,
  fetchAdapterSchemas,
  selectAdapterSchema,
  type AdapterSchemaDocument,
  type AdapterSchemaSelection,
} from "./adapterSchemas";

type Navigate = (to: string, opts?: { replace?: boolean }) => void;

const PROVIDER_LABELS: Record<string, string> = {
  anthropic: "Anthropic",
  openai: "OpenAI",
  google: "Google",
  deepseek: "DeepSeek",
};

export function AdapterSchemasPage({
  selection,
  onNavigate,
}: {
  selection: AdapterSchemaSelection;
  onNavigate: Navigate;
}) {
  const query = useQuery({
    queryKey: ["adapter-schemas"],
    queryFn: fetchAdapterSchemas,
    staleTime: Number.POSITIVE_INFINITY,
  });
  const selected = selectAdapterSchema(query.data ?? [], selection);

  return (
    <div className="h-full overflow-auto">
      <div className="mx-auto flex w-full max-w-[110rem] flex-col gap-density-6 p-density-4 md:p-density-6">
        <header className="max-w-4xl space-y-density-2">
          <p className="text-xs font-semibold uppercase tracking-wide text-primary">
            Captain · /adapter-schemas
          </p>
          <h1 className="text-2xl font-semibold tracking-tight">Adapter schemas</h1>
          <p className="text-sm text-muted-foreground">
            Inspect the native API, agent protocol, and CLI options Captain maps for every runtime.
            Read-only and managed options remain visible alongside JsonSchemaForm annotations.
          </p>
        </header>

        {query.isLoading ? (
          <StateMessage>Loading native runtime schemas...</StateMessage>
        ) : query.error ? (
          <StateMessage tone="error">{errorMessage(query.error)}</StateMessage>
        ) : selected && query.data ? (
          <AdapterSchemaWorkspace
            documents={query.data}
            selected={selected}
            onNavigate={onNavigate}
          />
        ) : (
          <StateMessage tone="error">The adapter schema catalog returned no schemas.</StateMessage>
        )}
      </div>
    </div>
  );
}

function AdapterSchemaWorkspace({
  documents,
  selected,
  onNavigate,
}: {
  documents: AdapterSchemaDocument[];
  selected: AdapterSchemaDocument;
  onNavigate: Navigate;
}) {
  const providerId = useId();
  const providers = useMemo(
    () => [...new Set(documents.map((document) => document.provider))],
    [documents],
  );
  const modes = documents.filter((document) => document.provider === selected.provider);
  const navigate = (provider: string, mode: string) =>
    onNavigate(adapterSchemaLocation({ provider, mode }));

  return (
    <div className="grid min-h-0 gap-density-4 lg:grid-cols-[16rem_minmax(0,1fr)]">
      <Panel title="Runtime">
        <div className="space-y-density-4">
          <Field label="Provider" htmlFor={providerId}>
            <Select
              id={providerId}
              value={selected.provider}
              options={providers.map((provider) => ({
                value: provider,
                label: providerLabel(provider),
              }))}
              onChange={(event) => {
                const first = documents.find(
                  (document) => document.provider === event.target.value,
                );
                if (first) navigate(first.provider, first.mode);
              }}
            />
          </Field>
          <Field label="Interface">
            <SegmentedControl
              aria-label="Interface"
              value={selected.mode}
              options={modes.map((document) => ({
                id: document.mode,
                label: modeLabel(document.mode),
              }))}
              onChange={(mode) => navigate(selected.provider, mode)}
              wrap
            />
          </Field>
          <p className="text-xs text-muted-foreground">
            {documents.length} schemas across {providers.length} providers
          </p>
        </div>
      </Panel>

      <Panel title="Schema">
        <div className="space-y-density-4">
          <div className="space-y-density-1">
            <h2 className="text-lg font-semibold">{schemaLabel(selected)}</h2>
            <p className="text-sm text-muted-foreground">{selected.description}</p>
            <p className="font-mono text-xs text-muted-foreground">{selected.title}</p>
          </div>
          <div className="min-h-[32rem] overflow-hidden rounded-md border border-border bg-card">
            <SchemaViewer
              key={`${selected.provider}/${selected.mode}`}
              schema={selected.schema}
              showControls
              defaultOpenDepth={1}
              className="min-h-[32rem]"
            />
          </div>
        </div>
      </Panel>
    </div>
  );
}

function schemaLabel(document: AdapterSchemaDocument): string {
  return `${providerLabel(document.provider)} ${modeLabel(document.mode)}`;
}

function providerLabel(provider: string): string {
  return PROVIDER_LABELS[provider] ?? provider;
}

function modeLabel(mode: string): string {
  return mode === "api" ? "API" : mode === "cli" ? "CLI" : mode.charAt(0).toUpperCase() + mode.slice(1);
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

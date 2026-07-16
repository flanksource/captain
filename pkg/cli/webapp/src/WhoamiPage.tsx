import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Button } from "@flanksource/clicky-ui/components";
import {
  Badge,
  UiCheck,
  UiCloud,
  UiFingerprint,
  UiKey,
  UiRefresh,
  UiTerminal,
  UiWarningTriangle,
} from "@flanksource/clicky-ui/data";

type WhoamiModel = {
  id: string;
  label?: string;
  releaseDate?: string;
  reasoning?: boolean;
  temperature?: boolean;
  inputMediaTypes?: string[];
  supportedEfforts?: string[];
  defaultEffort?: string;
  priority?: number;
};

type WhoamiAdapter = {
  backend: string;
  type: "api" | "cli";
  authenticated: boolean;
  authMethod?: string;
  authDetail?: string;
  binary?: string;
  binaryMissing?: string;
  modelCount: number;
  models?: string[];
  modelError?: string;
  modelDetails?: WhoamiModel[];
};

type WhoamiResult = {
  adapters: WhoamiAdapter[];
};

export function WhoamiPage() {
  const query = useQuery({
    queryKey: ["whoami", "full"],
    queryFn: fetchWhoami,
  });

  return (
    <div className="h-full overflow-auto">
      <div className="mx-auto flex w-full max-w-7xl flex-col gap-density-4 p-density-4 md:p-density-6">
        <header className="flex flex-wrap items-start justify-between gap-density-3 border-b border-border pb-density-3">
          <div className="flex min-w-0 items-start gap-density-3">
            <UiFingerprint className="mt-0.5 size-6 shrink-0 text-muted-foreground" />
            <div>
              <h1 className="text-lg font-semibold">AI adapters</h1>
              <p className="text-sm text-muted-foreground">
                Full whoami status for API providers and local agent runtimes.
              </p>
            </div>
          </div>
          <Button
            size="sm"
            variant="outline"
            disabled={query.isFetching}
            onClick={() => void query.refetch()}
          >
            <UiRefresh className={query.isFetching ? "animate-spin" : undefined} />
            Refresh
          </Button>
        </header>

        {query.isLoading ? (
          <StateMessage>Probing adapters and model catalogs...</StateMessage>
        ) : query.error ? (
          <StateMessage tone="error">{errorMessage(query.error)}</StateMessage>
        ) : (
          <WhoamiContent adapters={query.data?.adapters ?? []} />
        )}
      </div>
    </div>
  );
}

function WhoamiContent({ adapters }: { adapters: WhoamiAdapter[] }) {
  if (adapters.length === 0) return <StateMessage>No adapters were reported.</StateMessage>;

  const readyCount = adapters.filter(adapterReady).length;
  const modelCount = adapters.reduce((total, adapter) => total + adapter.modelCount, 0);
  return (
    <>
      <div className="flex flex-wrap gap-density-2 text-xs text-muted-foreground">
        <Badge variant="outline">{adapters.length} adapters</Badge>
        <Badge variant="outline" tone="success">{readyCount} ready</Badge>
        <Badge variant="outline" tone="info">{modelCount} model entries</Badge>
      </div>
      <AdapterGroup
        title="API providers"
        description="Direct provider APIs authenticated with environment keys."
        type="api"
        adapters={adapters}
      />
      <AdapterGroup
        title="CLI agents"
        description="Installed local runtimes authenticated through their own login state."
        type="cli"
        adapters={adapters}
      />
    </>
  );
}

function AdapterGroup({
  title,
  description,
  type,
  adapters,
}: {
  title: string;
  description: string;
  type: WhoamiAdapter["type"];
  adapters: WhoamiAdapter[];
}) {
  const rows = adapters.filter((adapter) => adapter.type === type);
  if (rows.length === 0) return null;
  const GroupIcon = type === "api" ? UiCloud : UiTerminal;

  return (
    <section className="grid gap-density-3" aria-labelledby={`whoami-${type}`}>
      <div className="flex items-center gap-density-2">
        <GroupIcon className="size-5 text-muted-foreground" />
        <div>
          <h2 id={`whoami-${type}`} className="text-base font-semibold">{title}</h2>
          <p className="text-xs text-muted-foreground">{description}</p>
        </div>
      </div>
      <div className="grid items-start gap-density-3 xl:grid-cols-2">
        {rows.map((adapter) => <AdapterCard key={adapter.backend} adapter={adapter} />)}
      </div>
    </section>
  );
}

function AdapterCard({ adapter }: { adapter: WhoamiAdapter }) {
  const ready = adapterReady(adapter);
  const models = adapterModels(adapter);
  return (
    <article className="overflow-hidden rounded-lg border border-border bg-background">
      <div className="flex flex-wrap items-center justify-between gap-density-2 border-b border-border bg-muted/30 px-density-3 py-density-2">
        <div className="flex items-center gap-density-2">
          {ready ? (
            <UiCheck className="size-5 text-green-600" />
          ) : (
            <UiWarningTriangle className="size-5 text-amber-600" />
          )}
          <h3 className="font-mono text-sm font-semibold">{adapter.backend}</h3>
          <Badge size="xs" variant="outline">{adapter.type.toUpperCase()}</Badge>
        </div>
        <Badge size="xs" tone={ready ? "success" : "warning"}>
          {ready ? "Ready" : adapter.authenticated ? "Unavailable" : "Needs setup"}
        </Badge>
      </div>

      <div className="grid gap-density-3 p-density-3">
        <AdapterDetails adapter={adapter} />
        {adapter.modelError && (
          <div className="rounded-md border border-amber-300/60 bg-amber-50/60 px-density-3 py-density-2 text-xs text-amber-900 dark:bg-amber-950/20 dark:text-amber-200">
            {adapter.modelError}
          </div>
        )}
        <div className="grid gap-density-2">
          <div className="flex items-center justify-between gap-density-2">
            <h4 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">Models</h4>
            <span className="text-xs text-muted-foreground">{adapter.modelCount}</span>
          </div>
          {models.length > 0 ? (
            <ul className="divide-y divide-border rounded-md border border-border">
              {models.map((model) => <ModelRow key={model.id} model={model} />)}
            </ul>
          ) : (
            <div className="rounded-md border border-dashed border-border px-density-3 py-density-2 text-xs text-muted-foreground">
              No models reported.
            </div>
          )}
        </div>
      </div>
    </article>
  );
}

function AdapterDetails({ adapter }: { adapter: WhoamiAdapter }) {
  return (
    <dl className="grid gap-x-density-4 gap-y-density-2 text-xs sm:grid-cols-[7rem_minmax(0,1fr)]">
      <Detail label="Authentication" value={adapter.authMethod || "Not configured"} icon={<UiKey />} />
      {adapter.authDetail && <Detail label="Identity" value={adapter.authDetail} mono />}
      {adapter.type === "cli" && (
        <Detail
          label="Binary"
          value={adapter.binary || `${adapter.binaryMissing || adapter.backend} not in PATH`}
          icon={<UiTerminal />}
          mono
        />
      )}
    </dl>
  );
}

function Detail({
  label,
  value,
  icon,
  mono,
}: {
  label: string;
  value: string;
  icon?: ReactNode;
  mono?: boolean;
}) {
  return (
    <>
      <dt className="flex items-center gap-density-1 text-muted-foreground">{icon}{label}</dt>
      <dd className={`min-w-0 break-all ${mono ? "font-mono" : ""}`}>{value}</dd>
    </>
  );
}

function ModelRow({ model }: { model: WhoamiModel }) {
  return (
    <li className="grid gap-density-2 px-density-3 py-density-2 text-xs">
      <div className="flex flex-wrap items-start justify-between gap-density-2">
        <div className="min-w-0">
          <div className="font-medium">{model.label || model.id}</div>
          <div className="break-all font-mono text-muted-foreground">{model.id}</div>
        </div>
        {model.releaseDate && <Badge size="xs" variant="outline">{model.releaseDate}</Badge>}
      </div>
      <div className="flex flex-wrap items-center gap-density-1 text-muted-foreground">
        {model.reasoning && <Badge size="xxs" variant="outline">reasoning</Badge>}
        {model.temperature && <Badge size="xxs" variant="outline">temperature</Badge>}
        {model.supportedEfforts && model.supportedEfforts.length > 0 && (
          <span>Effort: <span className="text-foreground">{model.supportedEfforts.join(" / ")}</span></span>
        )}
        {model.defaultEffort && <span>Default: {model.defaultEffort}</span>}
        {model.inputMediaTypes && model.inputMediaTypes.length > 0 && (
          <span>{model.inputMediaTypes.join(", ")}</span>
        )}
      </div>
    </li>
  );
}

function StateMessage({
  children,
  tone = "neutral",
}: {
  children: ReactNode;
  tone?: "neutral" | "error";
}) {
  const classes = tone === "error"
    ? "border-destructive/30 bg-destructive/10 text-destructive"
    : "border-border bg-muted/30 text-muted-foreground";
  return <div className={`rounded-md border px-density-4 py-density-3 text-sm ${classes}`}>{children}</div>;
}

function adapterReady(adapter: WhoamiAdapter) {
  return adapter.authenticated && (adapter.type !== "cli" || Boolean(adapter.binary));
}

function adapterModels(adapter: WhoamiAdapter): WhoamiModel[] {
  if (adapter.modelDetails && adapter.modelDetails.length > 0) return adapter.modelDetails;
  return (adapter.models ?? []).map((id) => ({ id }));
}

async function fetchWhoami(): Promise<WhoamiResult> {
  const response = await fetch("/api/v1/whoami?models=true&limit=0", {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: "{}",
  });
  if (!response.ok) {
    const message = await response.text();
    throw new Error(message || `Whoami failed with ${response.status}`);
  }
  return (await response.json()) as WhoamiResult;
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

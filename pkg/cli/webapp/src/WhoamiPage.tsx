import { useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Button, Switch } from "@flanksource/clicky-ui/components";
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
import {
  ProviderDefaultsControls,
  type ProviderAdapter,
  type ProviderDefault,
  type ProviderModel,
} from "./ProviderDefaultsControls";
import {
  DisabledControls,
  useDisabledSelections,
  type DisabledAxes,
  type DisabledController,
  type DisabledSelections,
} from "./DisabledControls";

type WhoamiModel = ProviderModel & {
  label?: string;
  releaseDate?: string;
  reasoning?: boolean;
  temperature?: boolean;
  inputMediaTypes?: string[];
  supportedEfforts?: string[];
  defaultEffort?: string;
  priority?: number;
  disabled?: boolean;
};

type WhoamiAdapter = ProviderAdapter & {
  authMethod?: string;
  authDetail?: string;
  binary?: string;
  binaryMissing?: string;
  modelError?: string;
  modelDetails?: WhoamiModel[];
  disabled?: boolean;
  disabledReason?: string;
};

type WhoamiResult = {
  adapters: WhoamiAdapter[];
  defaultProvider: string;
  providerDefaults: Record<string, ProviderDefault>;
  disabled?: DisabledSelections;
  axes?: DisabledAxes;
};

const NO_AXES: DisabledAxes = { modes: [], providers: [], efforts: [] };

type ProviderTokenResult = {
  provider: string;
  valid: boolean;
  saved: boolean;
  source: string;
  maskedToken: string;
  modelCount: number;
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
          <WhoamiContent
            result={query.data ?? { adapters: [], defaultProvider: "", providerDefaults: {} }}
            onRefresh={async () => { await query.refetch(); }}
          />
        )}
      </div>
    </div>
  );
}

function WhoamiContent({ result, onRefresh }: { result: WhoamiResult; onRefresh: () => Promise<void> }) {
  // No fallback provider: the active card is whichever backend the server named,
  // and guessing "anthropic" when it named none marked a card the user never chose.
  const { adapters, defaultProvider = "", providerDefaults = {}, axes = NO_AXES } = result;
  const controller = useDisabledSelections(result.disabled, onRefresh);
  if (adapters.length === 0) return <StateMessage>No adapters were reported.</StateMessage>;

  const readyCount = adapters.filter(adapterReady).length;
  const modelCount = adapters.reduce((total, adapter) => total + adapter.modelCount, 0);
  const disabledCount = adapters.filter((adapter) => adapter.disabled).length;
  return (
    <>
      <div className="flex flex-wrap gap-density-2 text-xs text-muted-foreground">
        <Badge variant="outline">{adapters.length} adapters</Badge>
        <Badge variant="outline" tone="success">{readyCount} ready</Badge>
        <Badge variant="outline" tone="info">{modelCount} model entries</Badge>
        {disabledCount > 0 && <Badge variant="outline" tone="warning">{disabledCount} disabled</Badge>}
      </div>
      <DisabledControls axes={axes} controller={controller} />
      <AdapterGroup
        title="API providers"
        description="Direct provider APIs authenticated with Captain vault tokens or environment keys."
        type="api"
        adapters={adapters}
        defaultProvider={defaultProvider}
        providerDefaults={providerDefaults}
        controller={controller}
        onRefresh={onRefresh}
      />
      <AdapterGroup
        title="CLI agents"
        description="Installed local runtimes authenticated through their own login state."
        type="cli"
        adapters={adapters}
        defaultProvider={defaultProvider}
        providerDefaults={providerDefaults}
        controller={controller}
        onRefresh={onRefresh}
      />
    </>
  );
}

function AdapterGroup({
  title,
  description,
  type,
  adapters,
  defaultProvider,
  providerDefaults,
  controller,
  onRefresh,
}: {
  title: string;
  description: string;
  type: WhoamiAdapter["type"];
  adapters: WhoamiAdapter[];
  defaultProvider: string;
  providerDefaults: Record<string, ProviderDefault>;
  controller: DisabledController;
  onRefresh: () => Promise<void>;
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
        {rows.map((adapter) => (
          <AdapterCard
            key={adapter.backend}
            adapter={adapter}
            adapters={adapters}
            defaults={providerDefaults[adapter.backend]}
            active={defaultProvider === adapter.backend}
            controller={controller}
            onRefresh={onRefresh}
          />
        ))}
      </div>
    </section>
  );
}

function AdapterCard({
  adapter,
  adapters,
  defaults,
  active,
  controller,
  onRefresh,
}: {
  adapter: WhoamiAdapter;
  adapters: WhoamiAdapter[];
  defaults?: ProviderDefault;
  active: boolean;
  controller: DisabledController;
  onRefresh: () => Promise<void>;
}) {
  const ready = adapterReady(adapter);
  const models = adapterModels(adapter);
  // A card switched off by its own mode or provider stays visibly off but is not
  // interactive: the axis that turned it off is where it turns back on.
  const inherited = Boolean(adapter.disabled) && !controller.isOff("backends", adapter.backend);
  return (
    <article className={`overflow-hidden rounded-lg border border-border bg-background ${adapter.disabled ? "opacity-60" : ""}`}>
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
        <div className="flex items-center gap-density-2">
          {inherited && adapter.disabledReason && (
            <span className="text-xs text-muted-foreground">off via {adapter.disabledReason}</span>
          )}
          <Switch
            checked={!adapter.disabled}
            disabled={inherited || controller.pending !== null}
            aria-label={`Enable ${adapter.backend}`}
            onChange={(checked) => void controller.setEnabled("backends", adapter.backend, checked)}
          />
          <Badge size="xs" tone={ready ? "success" : "warning"}>
            {adapter.disabled ? "Disabled" : ready ? "Ready" : adapter.authenticated ? "Unavailable" : "Needs setup"}
          </Badge>
        </div>
      </div>

      <div className="grid gap-density-3 p-density-3">
        <AdapterDetails adapter={adapter} />
        {adapter.type === "api" && <ProviderTokenControls adapter={adapter} onRefresh={onRefresh} />}
        {adapter.type === "api" && defaults && (
          <ProviderDefaultsControls
            key={`${defaults.agent}:${defaults.model}:${defaults.effort}:${active}`}
            provider={adapter.backend}
            defaults={defaults}
            adapters={adapters}
            active={active}
            onRefresh={onRefresh}
          />
        )}
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
              {models.map((model) => (
                <ModelRow key={model.id} model={model} backend={adapter.backend} controller={controller} />
              ))}
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

function ProviderTokenControls({ adapter, onRefresh }: { adapter: WhoamiAdapter; onRefresh: () => Promise<void> }) {
  const [token, setToken] = useState("");
  const [pending, setPending] = useState<"save" | "test" | null>(null);
  const [status, setStatus] = useState<{ tone: "success" | "error"; text: string } | null>(null);

  async function submit(action: "save" | "test") {
    setPending(action);
    setStatus(null);
    try {
      const result = await updateProviderToken(adapter.backend, action, token);
      if (action === "save") {
        setToken("");
        setStatus({ tone: "success", text: `Token saved and validated against ${result.modelCount} ${pluralModels(result.modelCount)}.` });
        await onRefresh();
      } else {
        setStatus({ tone: "success", text: `Current token is valid for ${result.modelCount} ${pluralModels(result.modelCount)}.` });
      }
    } catch (error) {
      setStatus({ tone: "error", text: errorMessage(error) });
    } finally {
      setPending(null);
    }
  }

  return (
    <div className="grid gap-density-2 rounded-md border border-border bg-muted/20 p-density-3">
      <div>
        <h4 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          Configure API token
        </h4>
        <p className="mt-density-1 text-xs text-muted-foreground">
          New tokens are tested before replacing the current credential.
        </p>
      </div>
      <label className="grid gap-density-1 text-xs font-medium" htmlFor={`provider-token-${adapter.backend}`}>
        API token
        <input
          id={`provider-token-${adapter.backend}`}
          aria-label={`${adapter.backend} API token`}
          type="password"
          autoComplete="new-password"
          value={token}
          disabled={pending !== null}
          onChange={(event) => setToken(event.target.value)}
          placeholder="Enter a replacement token"
          className="h-9 rounded-md border border-input bg-background px-density-3 font-mono text-sm outline-none focus:ring-2 focus:ring-ring disabled:opacity-50"
        />
      </label>
      <div className="flex flex-wrap gap-density-2">
        <Button
          size="sm"
          disabled={pending !== null || token.trim() === ""}
          aria-label={`Save & test ${adapter.backend} token`}
          onClick={() => void submit("save")}
        >
          {pending === "save" ? "Testing..." : "Save & test"}
        </Button>
        <Button
          size="sm"
          variant="outline"
          disabled={pending !== null}
          aria-label={`Test current ${adapter.backend} token`}
          onClick={() => void submit("test")}
        >
          {pending === "test" ? "Testing..." : "Test current"}
        </Button>
      </div>
      {status && (
        <div className={status.tone === "error" ? "text-xs text-destructive" : "text-xs text-green-700 dark:text-green-400"}>
          {status.text}
        </div>
      )}
    </div>
  );
}

async function updateProviderToken(backend: string, action: "save" | "test", token: string): Promise<ProviderTokenResult> {
  const test = action === "test";
  const response = await fetch(
    `/api/captain/ai/providers/${encodeURIComponent(backend)}/token${test ? "/test" : ""}`,
    {
      method: test ? "POST" : "PUT",
      headers: { Accept: "application/json", "Content-Type": "application/json" },
      body: test ? "{}" : JSON.stringify({ token }),
    },
  );
  if (!response.ok) {
    const message = (await response.text()).trim();
    throw new Error(message || `Token validation failed with ${response.status}`);
  }
  return (await response.json()) as ProviderTokenResult;
}

function pluralModels(count: number) {
  return count === 1 ? "model" : "models";
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

function ModelRow({
  model,
  backend,
  controller,
}: {
  model: WhoamiModel;
  backend: string;
  controller: DisabledController;
}) {
  // The server marks every model of a disabled backend; only the row's own entry
  // is switchable here, so an inherited disable renders read-only.
  const own = controller.isOff("models", `${backend}/${model.id}`) || controller.isOff("models", model.id);
  const inherited = Boolean(model.disabled) && !own;
  return (
    <li className={`grid gap-density-2 px-density-3 py-density-2 text-xs ${model.disabled ? "opacity-60" : ""}`}>
      <div className="flex flex-wrap items-start justify-between gap-density-2">
        <div className="min-w-0">
          <div className="font-medium">{model.label || model.id}</div>
          <div className="break-all font-mono text-muted-foreground">{model.id}</div>
        </div>
        <div className="flex items-center gap-density-2">
          {model.releaseDate && <Badge size="xs" variant="outline">{model.releaseDate}</Badge>}
          <Switch
            checked={!model.disabled}
            disabled={inherited || controller.pending !== null}
            aria-label={`Enable ${backend}/${model.id}`}
            onChange={(checked) => void controller.setModelEnabled(backend, model.id, checked)}
          />
        </div>
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
  const response = await fetch("/api/v1/whoami?models=true&limit=0&disabled=true", {
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

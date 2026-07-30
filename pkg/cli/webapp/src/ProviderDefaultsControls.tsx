import { useMemo, useReducer, useState } from "react";
import { Button } from "@flanksource/clicky-ui/components";

export type ProviderModel = {
  id: string;
  label?: string;
  supportedEfforts?: string[];
  defaultEffort?: string;
  disabled?: boolean;
};

export type ProviderAdapter = {
  backend: string;
  type: "api" | "cli";
  provider?: string;
  mode?: string;
  authenticated: boolean;
  binary?: string;
  modelCount: number;
  models?: string[];
  modelDetails?: ProviderModel[];
  disabled?: boolean;
};

export type ProviderDefault = {
  agent: string;
  model: string;
  effort: string;
  configured: boolean;
};

export function ProviderDefaultsControls({
  defaults,
  ...props
}: {
  provider: string;
  defaults: ProviderDefault;
  adapters: ProviderAdapter[];
  active: boolean;
  onRefresh: () => Promise<void>;
}) {
  return (
    <ProviderDefaultsForm
      key={`${defaults.agent}:${defaults.model}:${defaults.effort}`}
      defaults={defaults}
      {...props}
    />
  );
}

function ProviderDefaultsForm({
  provider,
  defaults,
  adapters,
  active,
  onRefresh,
}: {
  provider: string;
  defaults: ProviderDefault;
  adapters: ProviderAdapter[];
  active: boolean;
  onRefresh: () => Promise<void>;
}) {
  const [selection, updateSelection] = useReducer(
    (
      current: Pick<ProviderDefault, "agent" | "model" | "effort">,
      next: Partial<Pick<ProviderDefault, "agent" | "model" | "effort">>,
    ) => ({ ...current, ...next }),
    defaults,
  );
  const { agent, model, effort } = selection;
  const [pending, setPending] = useState<"defaults" | "active" | null>(null);
  const [status, setStatus] = useState<{ tone: "success" | "error"; text: string } | null>(null);
  // Agents and efforts come from the probe rather than a hardcoded table, so a
  // backend or effort the user disabled is simply not offered here.
  const agentOptions = useMemo(() => agentsForProvider(adapters, provider, agent), [adapters, provider, agent]);
  const models = useMemo(() => modelsForAgent(adapters, agent, model), [adapters, agent, model]);
  const selectedModel = models.find((candidate) => candidate.id === model);
  const effortOptions = selectedModel?.supportedEfforts ?? [];
  const modelAvailable = models.some((candidate) => candidate.id === model && candidate.available);

  function changeAgent(nextAgent: string) {
    const nextModel = modelsForAgent(adapters, nextAgent, "").find((candidate) => candidate.available);
    updateSelection({
      agent: nextAgent,
      model: nextModel?.id ?? "",
      effort: nextModel?.defaultEffort ?? "",
    });
    setStatus(null);
  }

  function changeModel(nextModel: string) {
    updateSelection({
      model: nextModel,
      effort: models.find((candidate) => candidate.id === nextModel)?.defaultEffort ?? "",
    });
    setStatus(null);
  }

  async function saveDefaults() {
    setPending("defaults");
    setStatus(null);
    try {
      await putJSON(`/api/captain/ai/providers/${encodeURIComponent(provider)}/defaults`, { agent, model, effort });
      setStatus({ tone: "success", text: "Provider defaults saved." });
      await onRefresh();
    } catch (error) {
      setStatus({ tone: "error", text: errorMessage(error) });
    } finally {
      setPending(null);
    }
  }

  async function setActiveProvider() {
    setPending("active");
    setStatus(null);
    try {
      await putJSON("/api/captain/ai/default-provider", { provider });
      setStatus({ tone: "success", text: `${provider} is now the default provider.` });
      await onRefresh();
    } catch (error) {
      setStatus({ tone: "error", text: errorMessage(error) });
    } finally {
      setPending(null);
    }
  }

  return (
    <div className="grid gap-density-2 rounded-md border border-border bg-muted/20 p-density-3">
      <div className="flex flex-wrap items-start justify-between gap-density-2">
        <div>
          <h4 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">Provider defaults</h4>
          <p className="mt-density-1 text-xs text-muted-foreground">Used only when a run leaves the field unspecified.</p>
        </div>
        {active ? (
          <span className="text-xs font-medium text-green-700 dark:text-green-400">Active default</span>
        ) : (
          <Button size="sm" variant="outline" disabled={pending !== null} onClick={() => void setActiveProvider()}>
            {pending === "active" ? "Saving..." : "Set as default"}
          </Button>
        )}
      </div>
      <label className="grid gap-density-1 text-xs font-medium">
        Default agent
        <select
          aria-label={`${provider} default agent`}
          value={agent}
          disabled={pending !== null || agentOptions.length === 1}
          onChange={(event) => changeAgent(event.target.value)}
          className="h-9 rounded-md border border-input bg-background px-density-2 text-sm"
        >
          {agentOptions.map((candidate) => (
            <option key={candidate} value={candidate}>{agentLabel(candidate, adapters)}</option>
          ))}
        </select>
      </label>
      <label className="grid gap-density-1 text-xs font-medium">
        Default model
        <select
          aria-label={`${provider} default model`}
          value={model}
          disabled={pending !== null || models.length === 0}
          onChange={(event) => changeModel(event.target.value)}
          className="h-9 rounded-md border border-input bg-background px-density-2 text-sm"
        >
          {models.map((candidate) => (
            <option key={candidate.id} value={candidate.id}>{candidate.label ?? candidate.id}{candidate.available ? "" : " (unavailable)"}</option>
          ))}
        </select>
      </label>
      <label className="grid gap-density-1 text-xs font-medium">
        Default effort
        <select
          aria-label={`${provider} default effort`}
          value={effort}
          disabled={pending !== null}
          onChange={(event) => { updateSelection({ effort: event.target.value }); setStatus(null); }}
          className="h-9 rounded-md border border-input bg-background px-density-2 text-sm"
        >
          <option value="">Model/provider default</option>
          {effortOptions.map((candidate) => <option key={candidate} value={candidate}>{candidate}</option>)}
        </select>
      </label>
      <div>
        <Button size="sm" disabled={pending !== null || !modelAvailable} onClick={() => void saveDefaults()}>
          {pending === "defaults" ? "Saving..." : "Save defaults"}
        </Button>
      </div>
      {!modelAvailable && (
        <p className="text-xs text-amber-700 dark:text-amber-300">Configure this runtime first to load a valid model catalog.</p>
      )}
      {status && <p className={status.tone === "error" ? "text-xs text-destructive" : "text-xs text-green-700 dark:text-green-400"}>{status.text}</p>}
    </div>
  );
}

type ModelOption = ProviderModel & { available: boolean };

/**
 * agentsForProvider lists the provider's enabled backends from the probe. The
 * currently-saved agent is kept even when disabled, so the select still shows
 * what is configured rather than silently rewriting it.
 */
function agentsForProvider(adapters: ProviderAdapter[], provider: string, current: string): string[] {
  const options = adapters
    .filter((adapter) => adapter.provider === provider && !adapter.disabled)
    .map((adapter) => adapter.backend);
  if (current && !options.includes(current)) options.unshift(current);
  return options.length > 0 ? options : [provider];
}

function modelsForAgent(adapters: ProviderAdapter[], agent: string, current: string): ModelOption[] {
  const adapter = adapters.find((candidate) => candidate.backend === agent);
  const models = (adapter?.modelDetails?.length
    ? adapter.modelDetails
    : (adapter?.models ?? []).map((id) => ({ id }) as ProviderModel)
  ).filter((model) => !model.disabled);
  const options = models.map((model) => ({ ...model, available: true }));
  if (current && !options.some((candidate) => candidate.id === current)) {
    options.unshift({ id: current, available: false });
  }
  return options;
}

function agentLabel(agent: string, adapters: ProviderAdapter[]) {
  const adapter = adapters.find((candidate) => candidate.backend === agent);
  const ready = adapter?.authenticated && (adapter.type === "api" || Boolean(adapter.binary));
  return `${agent}${ready ? " — ready" : " — needs setup"}`;
}

async function putJSON(path: string, body: unknown) {
  const response = await fetch(path, {
    method: "PUT",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    const message = (await response.text()).trim();
    throw new Error(message || `Configuration failed with ${response.status}`);
  }
  return response.json() as Promise<unknown>;
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

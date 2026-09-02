import { useState } from "react";
import { Badge } from "@flanksource/clicky-ui/data";
import { useDisabledSelections, type DisabledAxes, type DisabledController } from "./DisabledControls";
import {
  adapterReady,
  buildRuntimeFamilies,
  initialModel,
  initialRuntime,
  requiredFirstRuntime,
  runtimeModelKey,
  type RuntimeAdapter,
  type RuntimeFamily,
  type WhoamiModel,
  type WhoamiResult,
} from "./WhoamiModel";
import { availabilityPolicy, type AvailabilityPolicy } from "./WhoamiPolicy";
import { CapabilityPicker } from "./WhoamiPicker";
import { ProviderTreeNode, type TreeSelection } from "./WhoamiTree";
import { StateMessage } from "./StateMessage";
import { WhoamiProviderToken } from "./WhoamiProviderToken";

const NO_AXES: DisabledAxes = { modes: [], providers: [], efforts: [] };

export function WhoamiTopology({
  result,
  onRefresh,
}: {
  result: WhoamiResult;
  onRefresh: () => Promise<void>;
}) {
  const controller = useDisabledSelections(result.disabled, onRefresh);
  try {
    const families = buildRuntimeFamilies(result);
    if (families.length === 0) return <StateMessage>No runtimes were reported.</StateMessage>;
    return (
      <CapabilityTopology
        result={result}
        families={families}
        axes={result.axes ?? NO_AXES}
        controller={controller}
        onRefresh={onRefresh}
      />
    );
  } catch (error) {
    return <StateMessage tone="error">{error instanceof Error ? error.message : String(error)}</StateMessage>;
  }
}

function CapabilityTopology({
  result,
  families,
  axes,
  controller,
  onRefresh,
}: {
  result: WhoamiResult;
  families: RuntimeFamily[];
  axes: DisabledAxes;
  controller: DisabledController;
  onRefresh: () => Promise<void>;
}) {
  const seedRuntime = initialRuntime(result, families);
  const seedModel = initialModel(result, seedRuntime);
  const [runtimeKey, setRuntimeKey] = useState(seedRuntime.key);
  const [modelId, setModelId] = useState(seedModel?.id ?? "");
  const [selectedNode, setSelectedNode] = useState<TreeSelection>(seedModel
    ? { kind: "model", id: runtimeModelKey(seedRuntime.provider, seedRuntime.mode, seedModel.id) }
    : { kind: "runtime", id: seedRuntime.key });
  const [expandedProviders, setExpandedProviders] = useState(() => new Set([seedRuntime.provider]));
  const [expandedRuntimes, setExpandedRuntimes] = useState(() => new Set([seedRuntime.key]));
  const policy = availabilityPolicy(controller);
  const runtime = findRuntime(families, runtimeKey);
  const family = families.find((entry) => entry.provider === runtime.provider);
  if (!family) throw new Error(`Runtime ${runtime.key} has no provider family`);
  const model = runtime.models.find((entry) => entry.id === modelId);

  const selectFamily = (next: RuntimeFamily) => {
    const nextRuntime = preferredRuntime(next, result);
    setRuntimeKey(nextRuntime.key);
    setModelId(preferredModel(nextRuntime, result)?.id ?? "");
    setSelectedNode({ kind: "provider", id: next.provider });
    setExpandedProviders((current) => new Set(current).add(next.provider));
  };
  const selectRuntime = (next: RuntimeAdapter) => {
    setRuntimeKey(next.key);
    setModelId(preferredModel(next, result)?.id ?? "");
    setSelectedNode({ kind: "runtime", id: next.key });
    setExpandedProviders((current) => new Set(current).add(next.provider));
    setExpandedRuntimes((current) => new Set(current).add(next.key));
  };
  const selectModel = (nextRuntime: RuntimeAdapter, nextModel: WhoamiModel) => {
    setRuntimeKey(nextRuntime.key);
    setModelId(nextModel.id);
    setSelectedNode({
      kind: "model",
      id: runtimeModelKey(nextRuntime.provider, nextRuntime.mode, nextModel.id),
    });
    setExpandedProviders((current) => new Set(current).add(nextRuntime.provider));
    setExpandedRuntimes((current) => new Set(current).add(nextRuntime.key));
  };

  return (
    <div className="grid gap-density-4">
      <div>
        <h2 className="text-base font-semibold">AI adapters</h2>
        <p className="text-xs text-muted-foreground">
          Expand a provider, runtime mode, or model. Child selections remain intact when a parent policy is disabled.
        </p>
      </div>
      <AvailabilitySummary families={families} policy={policy} />
      <CapabilityPicker
        families={families}
        selectedRuntime={runtime}
        selectedModel={model}
        policy={policy}
        onSelect={selectModel}
      />
      <div className="grid items-start gap-density-4 lg:grid-cols-[minmax(0,1fr)_24rem]">
        <section className="overflow-hidden rounded-lg border border-border bg-background">
          <header className="border-b border-border bg-muted/20 px-density-4 py-density-3">
            <h2 className="text-sm font-semibold">Runtime capability tree</h2>
            <p className="mt-density-1 text-xs text-muted-foreground">
              Availability is inherited down the tree without erasing child selections.
            </p>
          </header>
          <div role="tree" aria-label="Capability topology" className="grid gap-density-1 p-density-2">
            {families.map((entry) => (
              <ProviderTreeNode
                key={entry.provider}
                family={entry}
                selectedNode={selectedNode}
                expandedProviders={expandedProviders}
                expandedRuntimes={expandedRuntimes}
                policy={policy}
                pending={controller.pending !== null}
                onSelectFamily={selectFamily}
                onSelectRuntime={selectRuntime}
                onSelectModel={selectModel}
                onToggleProvider={(id) => setExpandedProviders((current) => toggleMember(current, id))}
                onToggleRuntime={(id) => setExpandedRuntimes((current) => toggleMember(current, id))}
                onProviderEnabled={(provider, enabled) =>
                  void controller.setEnabled("providers", provider, enabled)}
                onRuntimeEnabled={(next, enabled) =>
                  void controller.setRuntimeEnabled(next.provider, next.mode, enabled)}
                onModelEnabled={(next, nextModel, enabled) =>
                  void controller.setModelEnabled(next.provider, nextModel.id, enabled)}
              />
            ))}
          </div>
        </section>
        <TopologyInspector
          runtime={runtime}
          model={model}
          policy={policy}
          axes={axes}
          controller={controller}
          onRefresh={onRefresh}
        />
      </div>
      {controller.error && <StateMessage tone="error">{controller.error}</StateMessage>}
    </div>
  );
}

function AvailabilitySummary({ families, policy }: { families: RuntimeFamily[]; policy: AvailabilityPolicy }) {
  const runtimes = families.flatMap((family) => family.adapters);
  const providersEnabled = families.filter((family) => policy.providerEnabled(family.provider)).length;
  const runtimesSelectable = runtimes.filter((runtime) => policy.runtimeAvailable(runtime) && adapterReady(runtime)).length;
  const modelsSelectable = runtimes.reduce((total, runtime) => total + runtime.models.filter(
    (model) => policy.modelAvailable(runtime, model) && adapterReady(runtime),
  ).length, 0);
  return (
    <div className="flex flex-wrap gap-density-2" aria-label="Enabled catalog summary">
      <Badge variant="outline" tone="info" clickToCopy={false}>
        {providersEnabled}/{families.length} providers enabled
      </Badge>
      <Badge variant="outline" tone="success" clickToCopy={false}>
        {runtimesSelectable}/{runtimes.length} runtimes selectable
      </Badge>
      <Badge variant="outline" clickToCopy={false}>{modelsSelectable} models selectable</Badge>
    </div>
  );
}

function TopologyInspector({
  runtime,
  model,
  policy,
  axes,
  controller,
  onRefresh,
}: {
  runtime: RuntimeAdapter;
  model: WhoamiModel | undefined;
  policy: AvailabilityPolicy;
  axes: DisabledAxes;
  controller: DisabledController;
  onRefresh: () => Promise<void>;
}) {
  const status = availabilityStatus(runtime, model, policy, controller);
  const available = status === "Available to Captain";
  return (
    <aside className="grid gap-density-4 rounded-lg border border-border bg-background p-density-4 lg:sticky lg:top-4">
      <section>
        <h2 className="text-sm font-semibold">Resolved values</h2>
        <dl className="mt-density-3 grid gap-density-2 text-xs">
          <ResolvedValue label="Provider" value={runtime.provider} mono />
          <ResolvedValue label="Mode" value={runtime.label} />
          <ResolvedValue label="Model" value={model?.label ?? model?.id ?? "No model reported"} />
          <div className="grid grid-cols-[7rem_minmax(0,1fr)] items-center gap-density-2 border-t border-border pt-density-2">
            <dt className="text-muted-foreground">Availability</dt>
            <dd><Badge size="xs" tone={available ? "success" : "warning"} clickToCopy={false}>{status}</Badge></dd>
          </div>
          <ResolvedValue label="Authentication" value={runtime.authMethod || "Not configured"} />
          {runtime.authDetail && <ResolvedValue label="Identity" value={runtime.authDetail} mono />}
          {runtime.type === "cli" && (
            <ResolvedValue label="Binary" value={runtime.binary || `${runtime.binaryMissing || runtime.provider} not in PATH`} mono />
          )}
          <ResolvedValue label="Reported models" value={String(runtime.modelCount)} />
        </dl>
        {runtime.modelError && <p className="mt-density-3 text-xs text-amber-700 dark:text-amber-300">{runtime.modelError}</p>}
      </section>
      {runtime.type === "api" && (
        <WhoamiProviderToken key={runtime.key} runtime={runtime} onRefresh={onRefresh} />
      )}
      <GlobalPolicy axes={axes} controller={controller} />
    </aside>
  );
}

function GlobalPolicy({ axes, controller }: { axes: DisabledAxes; controller: DisabledController }) {
  return (
    <section className="grid gap-density-3 border-t border-border pt-density-3">
      <div>
        <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">Global policy</h3>
        <p className="mt-density-1 text-xs text-muted-foreground">Mode and effort switches apply across every provider.</p>
      </div>
      <PolicyRow label="Modes" values={axes.modes} enabled={(value) => !controller.isOff("modes", value)}
        onChange={(value, enabled) => void controller.setEnabled("modes", value, enabled)} pending={controller.pending !== null} />
      <PolicyRow label="Efforts" values={axes.efforts} enabled={(value) => !controller.isOff("efforts", value)}
        onChange={(value, enabled) => void controller.setEnabled("efforts", value, enabled)} pending={controller.pending !== null} />
    </section>
  );
}

function PolicyRow({
  label,
  values,
  enabled,
  onChange,
  pending,
}: {
  label: string;
  values: string[];
  enabled: (value: string) => boolean;
  onChange: (value: string, enabled: boolean) => void;
  pending: boolean;
}) {
  if (values.length === 0) return null;
  return (
    <fieldset className="grid gap-density-1">
      <legend className="text-xs font-medium">{label}</legend>
      <div className="flex flex-wrap gap-x-density-3 gap-y-density-1">
        {values.map((value) => (
          <label key={value} className="flex items-center gap-density-1 font-mono text-xs">
            <input type="checkbox" checked={enabled(value)} disabled={pending}
              aria-label={`Enable ${value} ${label.toLowerCase()} globally`}
              onChange={(event) => onChange(value, event.target.checked)} />
            {value}
          </label>
        ))}
      </div>
    </fieldset>
  );
}

function ResolvedValue({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="grid grid-cols-[7rem_minmax(0,1fr)] gap-density-2 border-t border-border pt-density-2 first:border-t-0 first:pt-0">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className={mono ? "break-all font-mono" : undefined}>{value}</dd>
    </div>
  );
}

function availabilityStatus(
  runtime: RuntimeAdapter,
  model: WhoamiModel | undefined,
  policy: AvailabilityPolicy,
  controller: DisabledController,
): string {
  if (!policy.providerEnabled(runtime.provider)) return "Excluded by provider policy";
  if (controller.isOff("modes", runtime.mode)) return "Excluded by mode policy";
  if (!policy.runtimeEnabled(runtime)) return "Excluded by runtime policy";
  if (!model) return "No model reported";
  if (!policy.modelEnabled(runtime, model)) return "Excluded by model policy";
  if (!runtime.authenticated) return "Needs setup";
  if (!adapterReady(runtime)) return "Runtime unavailable";
  return "Available to Captain";
}

function preferredRuntime(family: RuntimeFamily, result: WhoamiResult): RuntimeAdapter {
  const mode = result.providerDefaults[family.provider]?.mode;
  return family.adapters.find((runtime) => runtime.mode === mode) ?? requiredFirstRuntime(family);
}

function preferredModel(runtime: RuntimeAdapter, result: WhoamiResult): WhoamiModel | undefined {
  const id = result.providerDefaults[runtime.provider]?.model;
  return runtime.models.find((model) => model.id === id) ?? runtime.models[0];
}

function findRuntime(families: RuntimeFamily[], key: string): RuntimeAdapter {
  const runtime = families.flatMap((family) => family.adapters).find((entry) => entry.key === key);
  if (!runtime) throw new Error(`Selected runtime ${key} is absent from the runtime catalog`);
  return runtime;
}

function toggleMember(current: Set<string>, id: string): Set<string> {
  const next = new Set(current);
  if (next.has(id)) next.delete(id);
  else next.add(id);
  return next;
}

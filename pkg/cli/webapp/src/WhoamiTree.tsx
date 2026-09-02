import type { ReactNode } from "react";
import { AGENT_RUNTIME_ICONS, SESSION_TONES, type AgentRuntime } from "@flanksource/clicky-ui/ai";
import { providerIcon, providerIconColor } from "@flanksource/clicky-ui/chat";
import { Button } from "@flanksource/clicky-ui/components";
import { Badge } from "@flanksource/clicky-ui/data";
import { cn } from "@flanksource/clicky-ui/utils";
import { adapterReady, providerLabel, runtimeModelKey, type RuntimeAdapter, type RuntimeFamily, type RuntimeMode, type WhoamiModel } from "./WhoamiModel";
import type { AvailabilityPolicy } from "./WhoamiPolicy";

export type TreeSelection = {
  kind: "provider" | "runtime" | "model";
  id: string;
};

type TreeState = {
  selectedNode: TreeSelection;
  expandedProviders: Set<string>;
  expandedRuntimes: Set<string>;
  policy: AvailabilityPolicy;
  pending: boolean;
  onSelectFamily: (family: RuntimeFamily) => void;
  onSelectRuntime: (runtime: RuntimeAdapter) => void;
  onSelectModel: (runtime: RuntimeAdapter, model: WhoamiModel) => void;
  onToggleProvider: (provider: string) => void;
  onToggleRuntime: (runtime: string) => void;
  onProviderEnabled: (provider: string, enabled: boolean) => void;
  onRuntimeEnabled: (runtime: RuntimeAdapter, enabled: boolean) => void;
  onModelEnabled: (runtime: RuntimeAdapter, model: WhoamiModel, enabled: boolean) => void;
};

const RUNTIME_CLASS: Record<RuntimeMode, AgentRuntime> = {
  api: "api",
  agent: "sdk",
  cli: "terminal",
  cmux: "terminal",
};

export function ProviderTreeNode({ family, ...state }: TreeState & { family: RuntimeFamily }) {
  const expanded = state.expandedProviders.has(family.provider);
  const enabled = state.policy.providerEnabled(family.provider);
  const selected = state.selectedNode.kind === "provider" && state.selectedNode.id === family.provider;
  return (
    <div role="treeitem" aria-expanded={expanded} aria-selected={selected}>
      <TreeRow active={selected} available={enabled}>
        <DisclosureButton expanded={expanded} label={`${family.label} provider`}
          onClick={() => state.onToggleProvider(family.provider)} />
        <Button type="button" variant="ghost" onClick={() => state.onSelectFamily(family)}
          className="h-8 min-w-0 flex-1 justify-start px-density-2 text-left">
          <ProviderGlyph provider={family.provider} />
          <span className="min-w-0 flex-1 truncate text-xs font-semibold">{family.label}</span>
          <span className="shrink-0 text-[11px] font-normal text-muted-foreground">
            {family.adapters.length} runtimes · {family.modelCount} models
          </span>
        </Button>
        <AvailabilityCheckbox label={`Enable ${family.label} provider`} checked={enabled} disabled={state.pending}
          onChange={(checked) => state.onProviderEnabled(family.provider, checked)} />
      </TreeRow>
      {expanded && (
        <div role="group" className="ml-density-5 border-l border-border pl-density-2">
          {family.adapters.map((runtime) => <RuntimeTreeNode key={runtime.key} runtime={runtime} {...state} />)}
        </div>
      )}
    </div>
  );
}

function RuntimeTreeNode({ runtime, ...state }: TreeState & { runtime: RuntimeAdapter }) {
  const expanded = state.expandedRuntimes.has(runtime.key);
  const ownEnabled = state.policy.runtimeEnabled(runtime);
  const available = state.policy.runtimeAvailable(runtime);
  const hasModels = runtime.models.length > 0;
  const selected = state.selectedNode.kind === "runtime" && state.selectedNode.id === runtime.key;
  return (
    <div role="treeitem" aria-expanded={hasModels ? expanded : undefined} aria-selected={selected}>
      <TreeRow active={selected} available={available}>
        {hasModels ? (
          <DisclosureButton expanded={expanded} label={`${runtime.label} runtime`}
            onClick={() => state.onToggleRuntime(runtime.key)} />
        ) : <span className="size-7 shrink-0" />}
        <Button type="button" variant="ghost" onClick={() => state.onSelectRuntime(runtime)}
          className="h-8 min-w-0 flex-1 justify-start px-density-2 text-left">
          <RuntimeGlyph runtime={runtime} />
          <span className="min-w-0 flex-1 truncate text-xs font-semibold">{runtime.label}</span>
          <RuntimeStatus runtime={runtime} available={available} />
          {runtime.type === "api" && <APITokenStatus runtime={runtime} />}
        </Button>
        <AvailabilityCheckbox label={`Enable ${providerLabel(runtime.provider)} ${runtime.label} runtime`}
          checked={ownEnabled} disabled={state.pending}
          onChange={(checked) => state.onRuntimeEnabled(runtime, checked)} />
      </TreeRow>
      {hasModels && expanded && (
        <div role="group" className="ml-density-5 border-l border-border pl-density-2">
          {runtime.models.map((model) => <ModelTreeNode key={model.id} runtime={runtime} model={model} {...state} />)}
        </div>
      )}
    </div>
  );
}

function ModelTreeNode({ runtime, model, ...state }: TreeState & { runtime: RuntimeAdapter; model: WhoamiModel }) {
  const ownEnabled = state.policy.modelEnabled(runtime, model);
  const available = state.policy.modelAvailable(runtime, model);
  const selected = state.selectedNode.kind === "model" &&
    state.selectedNode.id === runtimeModelKey(runtime.provider, runtime.mode, model.id);
  return (
    <div role="treeitem" aria-selected={selected}>
      <TreeRow active={selected} available={available}>
        <span className="size-7 shrink-0" />
        <Button type="button" variant="ghost" onClick={() => state.onSelectModel(runtime, model)}
          className="h-8 min-w-0 flex-1 justify-start overflow-hidden px-density-2 text-left">
          <span className="min-w-0 flex-1 truncate text-xs font-semibold">{model.label || model.id}</span>
          <span className="min-w-0 truncate font-mono text-[11px] font-normal text-muted-foreground">{model.id}</span>
          {model.temperature && <Badge size="xxs" variant="outline" clickToCopy={false}>temperature</Badge>}
        </Button>
        <AvailabilityCheckbox label={`Enable ${model.id} model`} checked={ownEnabled} disabled={state.pending}
          onChange={(checked) => state.onModelEnabled(runtime, model, checked)} />
      </TreeRow>
    </div>
  );
}

function TreeRow({ active, available, children }: { active: boolean; available: boolean; children: ReactNode }) {
  return (
    <div className={cn(
      "flex h-9 min-w-0 items-center rounded-md border pr-density-2",
      active ? "border-primary/50 bg-primary/10" : "border-transparent",
      !available && "border-dashed opacity-60",
    )}>{children}</div>
  );
}

function DisclosureButton({ expanded, label, onClick }: { expanded: boolean; label: string; onClick: () => void }) {
  return (
    <Button type="button" size="icon" variant="ghost" aria-label={`${expanded ? "Collapse" : "Expand"} ${label}`}
      aria-expanded={expanded} onClick={onClick} className="size-7 shrink-0">
      {expanded ? "−" : "+"}
    </Button>
  );
}

function AvailabilityCheckbox({
  label,
  checked,
  disabled,
  onChange,
}: {
  label: string;
  checked: boolean;
  disabled: boolean;
  onChange: (checked: boolean) => void;
}) {
  return <input type="checkbox" aria-label={label} title={label} checked={checked} disabled={disabled}
    className="size-4 shrink-0 accent-primary" onChange={(event) => onChange(event.target.checked)} />;
}

export function ProviderGlyph({ provider }: { provider: string }) {
  const Glyph = providerIcon(provider);
  if (!Glyph) throw new Error(`No provider icon is registered for ${provider}`);
  return <Glyph className={cn("size-4 shrink-0", providerIconColor(provider))} aria-hidden="true" />;
}

export function RuntimeGlyph({ runtime }: { runtime: RuntimeAdapter }) {
  const meta = AGENT_RUNTIME_ICONS[RUNTIME_CLASS[runtime.mode]];
  const Glyph = meta.icon;
  return (
    <span className={cn("grid size-6 shrink-0 place-items-center rounded-full", SESSION_TONES[meta.tone].disc)}>
      <Glyph className="size-4" aria-hidden="true" />
    </span>
  );
}

function RuntimeStatus({ runtime, available }: { runtime: RuntimeAdapter; available: boolean }) {
  const ready = adapterReady(runtime);
  const label = !available ? "Disabled" : ready ? "Ready" : runtime.authenticated ? "Unavailable" : "Needs setup";
  return <Badge size="xxs" tone={available && ready ? "success" : "warning"} clickToCopy={false}>{label}</Badge>;
}

function APITokenStatus({ runtime }: { runtime: RuntimeAdapter }) {
  const valid = runtime.authenticated && !runtime.modelError;
  const label = valid ? "Token valid" : runtime.authenticated ? "Token unverified" : "No token";
  return <Badge size="xxs" tone={valid ? "success" : "warning"} clickToCopy={false}>{label}</Badge>;
}

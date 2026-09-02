import { WORKFLOW_PHASES } from "@flanksource/clicky-ui/ai";
import { TreePickerField } from "@flanksource/clicky-ui/components";
import { Badge } from "@flanksource/clicky-ui/data";
import { UiBrain } from "@flanksource/clicky-ui/icons";
import { cn } from "@flanksource/clicky-ui/utils";
import {
  adapterReady,
  providerLabel,
  runtimeModelKey,
  type RuntimeAdapter,
  type RuntimeFamily,
  type WhoamiModel,
} from "./WhoamiModel";
import type { AvailabilityPolicy } from "./WhoamiPolicy";
import { ProviderGlyph, RuntimeGlyph } from "./WhoamiTree";

type CapabilityNode = {
  key: string;
  kind: "provider" | "runtime" | "model";
  label: string;
  provider: string;
  runtime?: RuntimeAdapter;
  model?: WhoamiModel;
  children: CapabilityNode[];
};

export function CapabilityPicker({
  families,
  selectedRuntime,
  selectedModel,
  policy,
  onSelect,
}: {
  families: RuntimeFamily[];
  selectedRuntime: RuntimeAdapter;
  selectedModel: WhoamiModel | undefined;
  policy: AvailabilityPolicy;
  onSelect: (runtime: RuntimeAdapter, model: WhoamiModel) => void;
}) {
  const roots = pickerRoots(families);
  const selected = selectedModel
    ? findNode(roots, `model:${runtimeModelKey(selectedRuntime.provider, selectedRuntime.mode, selectedModel.id)}`)
    : null;
  const RunIcon = WORKFLOW_PHASES.run.icon;
  const selectable = (node: CapabilityNode): boolean => {
    if (node.kind !== "model") return false;
    const runtime = requiredRuntime(node);
    return adapterReady(runtime) && policy.modelAvailable(runtime, requiredModel(node));
  };

  return (
    <section className="flex flex-wrap items-end gap-density-4 rounded-lg border border-border bg-muted/20 p-density-3">
      <div className="min-w-64 flex-1 sm:max-w-lg">
        <div className="mb-density-1 text-xs font-medium">Run model</div>
        <TreePickerField<CapabilityNode>
          roots={roots}
          getKey={(node) => node.key}
          getChildren={(node) => node.children}
          defaultOpen={() => false}
          getSearchText={(node) =>
            `${node.label} ${node.provider} ${node.runtime?.mode ?? ""} ${node.model?.id ?? ""}`}
          isSelectable={selectable}
          onSelect={(node) => onSelect(requiredRuntime(node), requiredModel(node))}
          selected={selected}
          revealSelected
          showControls
          ariaLabel="Run model"
          label={
            <span className="flex min-w-0 items-center gap-density-2">
              <RunIcon className="size-4 shrink-0 text-primary" />
              <span className="shrink-0">Run model</span>
              <ProviderGlyph provider={selectedRuntime.provider} />
              <RuntimeGlyph runtime={selectedRuntime} />
              <span className="min-w-0 truncate font-semibold">
                {selectedModel?.label ?? selectedModel?.id ?? "No model reported"}
              </span>
              <span className="shrink-0 text-xs text-muted-foreground">
                via {providerLabel(selectedRuntime.provider)} / {selectedRuntime.label}
              </span>
            </span>
          }
          renderRow={(context) => (
            <CapabilityPickerRow {...context} available={selectable(context.node)} />
          )}
        />
      </div>
      <p className="max-w-xl text-xs text-muted-foreground">
        Browse provider → mode → model in a form field. Only ready models that remain enabled across all three levels can be selected.
      </p>
    </section>
  );
}

function CapabilityPickerRow({
  node,
  selected,
  available,
}: {
  node: CapabilityNode;
  selected: boolean;
  available: boolean;
}) {
  if (node.kind === "provider") {
    return (
      <span className="flex min-w-0 flex-1 items-center gap-density-2">
        <ProviderGlyph provider={node.provider} />
        <span className="min-w-0 flex-1 truncate font-semibold">{node.label}</span>
        <span className="text-[11px] text-muted-foreground">{node.children.length} runtimes</span>
      </span>
    );
  }
  if (node.kind === "runtime") {
    const runtime = requiredRuntime(node);
    return (
      <span className="flex min-w-0 flex-1 items-center gap-density-2">
        <RuntimeGlyph runtime={runtime} />
        <span className="min-w-0 flex-1 truncate text-xs font-semibold">{runtime.label}</span>
        <Badge size="xxs" tone={adapterReady(runtime) ? "success" : "warning"} clickToCopy={false}>
          {adapterReady(runtime) ? "Ready" : "Unavailable"}
        </Badge>
      </span>
    );
  }
  const model = requiredModel(node);
  return (
    <span className={cn("flex min-w-0 flex-1 items-center gap-density-2", !available && "opacity-50")}>
      <UiBrain className={cn("size-4 shrink-0", selected ? "text-primary" : "text-muted-foreground")} />
      <span className="min-w-0 flex-1 truncate">{model.label || model.id}</span>
      <span className="truncate font-mono text-[11px] text-muted-foreground">{model.id}</span>
      {!available && <Badge size="xxs" tone="warning" clickToCopy={false}>Unavailable</Badge>}
    </span>
  );
}

function pickerRoots(families: RuntimeFamily[]): CapabilityNode[] {
  return families.map((family) => ({
    key: `provider:${family.provider}`,
    kind: "provider",
    label: family.label,
    provider: family.provider,
    children: family.adapters.map((runtime) => ({
      key: `runtime:${runtime.key}`,
      kind: "runtime",
      label: runtime.label,
      provider: family.provider,
      runtime,
      children: runtime.models.map((model) => ({
        key: `model:${runtimeModelKey(runtime.provider, runtime.mode, model.id)}`,
        kind: "model",
        label: model.label || model.id,
        provider: family.provider,
        runtime,
        model,
        children: [],
      })),
    })),
  }));
}

function findNode(roots: CapabilityNode[], key: string): CapabilityNode {
  const pending = [...roots];
  while (pending.length > 0) {
    const node = pending.pop();
    if (!node) throw new Error("Capability picker traversal lost a node");
    if (node.key === key) return node;
    pending.push(...node.children);
  }
  throw new Error(`Capability picker node not found: ${key}`);
}

function requiredRuntime(node: CapabilityNode): RuntimeAdapter {
  if (!node.runtime) throw new Error(`Capability picker ${node.key} has no runtime`);
  return node.runtime;
}

function requiredModel(node: CapabilityNode): WhoamiModel {
  if (!node.model) throw new Error(`Capability picker ${node.key} has no model`);
  return node.model;
}

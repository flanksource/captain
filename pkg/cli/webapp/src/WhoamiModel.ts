import type { RuntimeCatalogFamily } from "@flanksource/clicky-ui/ai";
import type { ChatModel } from "@flanksource/clicky-ui/chat";

export type RuntimeMode = "api" | "cli" | "agent" | "cmux";

export type WhoamiModel = {
  id: string;
  label?: string;
  provider?: string;
  mode?: RuntimeMode;
  releaseDate?: string;
  reasoning?: boolean;
  temperature?: boolean;
  inputMediaTypes?: string[];
  supportedEfforts?: string[];
  defaultEffort?: string;
  disabled?: boolean;
};

export type WhoamiAdapter = {
  type: "api" | "cli";
  provider: string;
  mode: RuntimeMode;
  authenticated: boolean;
  authMethod?: string;
  authDetail?: string;
  binary?: string;
  binaryMissing?: string;
  dependencyMissing?: string;
  provisioner?: string;
  runtimeError?: string;
  modelCount: number;
  models?: string[];
  modelError?: string;
  modelDetails?: WhoamiModel[];
  disabled?: boolean;
  disabledReason?: string;
};

export type ProviderDefault = {
  mode: RuntimeMode;
  model: string;
  effort: string;
  configured: boolean;
};

export type RuntimeAdapter = Omit<WhoamiAdapter, "models"> & {
  key: string;
  label: string;
  models: WhoamiModel[];
};

export type RuntimeFamily = {
  provider: string;
  label: string;
  family: string;
  adapters: RuntimeAdapter[];
  modelCount: number;
};

export type WhoamiResult = {
  adapters: WhoamiAdapter[];
  defaultProvider: string;
  providerDefaults: Record<string, ProviderDefault>;
  disabled?: import("./DisabledControls").DisabledSelections;
  axes?: import("./DisabledControls").DisabledAxes;
  models: ChatModel[];
  runtimes: RuntimeCatalogFamily[];
};

const PROVIDER_LABELS: Record<string, string> = {
  anthropic: "Anthropic",
  openai: "OpenAI",
  google: "Google Gemini",
  deepseek: "DeepSeek",
};

export function runtimeKey(provider: string, mode: RuntimeMode): string {
  return `${provider}:${mode}`;
}

export function runtimeModelKey(provider: string, mode: RuntimeMode, model: string): string {
  return `${runtimeKey(provider, mode)}:${model}`;
}

export function modelPolicyKey(provider: string, model: string): string {
  return `${provider}/${model}`;
}

export function providerLabel(provider: string): string {
  const label = PROVIDER_LABELS[provider];
  if (!label) throw new Error(`No display label is registered for provider ${provider}`);
  return label;
}

export function adapterReady(
  adapter: Pick<
    WhoamiAdapter,
    "authenticated" | "disabled" | "type" | "binary" | "provisioner" | "dependencyMissing" | "runtimeError"
  >,
): boolean {
  if (!adapter.authenticated || adapter.disabled) return false;
  if (adapter.type === "api") return true;
  return Boolean(adapter.binary || adapter.provisioner) && !adapter.dependencyMissing && !adapter.runtimeError;
}

export function buildRuntimeFamilies(result: WhoamiResult): RuntimeFamily[] {
  if (result.runtimes.length === 0) throw new Error("Whoami response did not include the runtime catalog");
  const adapters = new Map<string, WhoamiAdapter>();
  for (const adapter of result.adapters) {
    const key = runtimeKey(adapter.provider, adapter.mode);
    if (adapters.has(key)) throw new Error(`Whoami response repeated runtime ${adapter.provider} ${adapter.mode}`);
    adapters.set(key, adapter);
  }

  const families = result.runtimes.map((family): RuntimeFamily => {
    const rows = family.modes.map((entry): RuntimeAdapter => {
      const key = runtimeKey(family.provider, entry.mode);
      const adapter = adapters.get(key);
      if (!adapter) throw new Error(`Whoami response omitted runtime probe ${family.provider} ${entry.mode}`);
      adapters.delete(key);
      return {
        ...adapter,
        key,
        label: entry.mode.toUpperCase(),
        models: adapterModels(adapter),
      };
    });
    return {
      provider: family.provider,
      label: providerLabel(family.provider),
      family: family.family,
      adapters: rows,
      modelCount: rows.reduce((total, adapter) => total + adapter.modelCount, 0),
    };
  });
  if (adapters.size > 0) throw new Error(`Whoami runtime catalog omitted ${[...adapters.keys()].join(", ")}`);
  return families;
}

export function initialRuntime(result: WhoamiResult, families: RuntimeFamily[]): RuntimeAdapter {
  const family = families.find((entry) => entry.provider === result.defaultProvider);
  if (!family) throw new Error(`Default provider ${result.defaultProvider} is absent from the runtime catalog`);
  const defaults = result.providerDefaults[family.provider];
  if (!defaults) return requiredFirstRuntime(family);
  const runtime = family.adapters.find((entry) => entry.mode === defaults.mode);
  if (!runtime) throw new Error(`Default runtime ${family.provider} ${defaults.mode} is absent from the runtime catalog`);
  return runtime;
}

export function initialModel(result: WhoamiResult, runtime: RuntimeAdapter): WhoamiModel | undefined {
  const defaults = result.providerDefaults[runtime.provider];
  return runtime.models.find((model) => model.id === defaults?.model) ?? runtime.models[0];
}

export function requiredFirstRuntime(family: RuntimeFamily): RuntimeAdapter {
  const runtime = family.adapters[0];
  if (!runtime) throw new Error(`Provider ${family.provider} has no runtimes`);
  return runtime;
}

function adapterModels(adapter: WhoamiAdapter): WhoamiModel[] {
  if (adapter.modelDetails && adapter.modelDetails.length > 0) return adapter.modelDetails;
  return (adapter.models ?? []).map((id) => ({ id }));
}

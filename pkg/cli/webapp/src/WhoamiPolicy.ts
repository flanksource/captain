import type { DisabledController } from "./DisabledControls";
import { modelPolicyKey, type RuntimeAdapter, type WhoamiModel } from "./WhoamiModel";

export type AvailabilityPolicy = {
  providerEnabled: (provider: string) => boolean;
  runtimeEnabled: (runtime: RuntimeAdapter) => boolean;
  modelEnabled: (runtime: RuntimeAdapter, model: WhoamiModel) => boolean;
  runtimeAvailable: (runtime: RuntimeAdapter) => boolean;
  modelAvailable: (runtime: RuntimeAdapter, model: WhoamiModel) => boolean;
};

export function availabilityPolicy(controller: DisabledController): AvailabilityPolicy {
  const providerEnabled = (provider: string) => !controller.isOff("providers", provider);
  const runtimeEnabled = (runtime: RuntimeAdapter) =>
    !controller.isRuntimeOff(runtime.provider, runtime.mode);
  const modelEnabled = (runtime: RuntimeAdapter, model: WhoamiModel) =>
    !controller.isOff("models", model.id) &&
    !controller.isOff("models", modelPolicyKey(runtime.provider, model.id));
  const runtimeAvailable = (runtime: RuntimeAdapter) =>
    providerEnabled(runtime.provider) &&
    !controller.isOff("modes", runtime.mode) &&
    runtimeEnabled(runtime);
  return {
    providerEnabled,
    runtimeEnabled,
    modelEnabled,
    runtimeAvailable,
    modelAvailable: (runtime, model) => runtimeAvailable(runtime) && modelEnabled(runtime, model),
  };
}

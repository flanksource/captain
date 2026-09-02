import { useCallback, useEffect, useRef, useState } from "react";

export type DisabledRuntime = {
  provider: string;
  mode: string;
};

export type DisabledSelections = {
  modes: string[] | null;
  providers: string[] | null;
  runtimes: DisabledRuntime[] | null;
  models: string[] | null;
  efforts: string[] | null;
};

export type DisabledAxes = {
  modes: string[];
  providers: string[];
  efforts: string[];
};

export type DisabledAxis = "modes" | "providers" | "models" | "efforts";

export type DisabledController = {
  selections: DisabledSelections;
  pending: string | null;
  error: string | null;
  isOff: (axis: DisabledAxis, token: string) => boolean;
  isRuntimeOff: (provider: string, mode: string) => boolean;
  setEnabled: (axis: DisabledAxis, token: string, enabled: boolean) => Promise<void>;
  setRuntimeEnabled: (provider: string, mode: string, enabled: boolean) => Promise<void>;
  setModelEnabled: (provider: string, id: string, enabled: boolean) => Promise<void>;
};

const EMPTY: DisabledSelections = {
  modes: null,
  providers: null,
  runtimes: null,
  models: null,
  efforts: null,
};

export function useDisabledSelections(
  initial: DisabledSelections | undefined,
  onRefresh: () => Promise<void>,
): DisabledController {
  const seed = initial ?? EMPTY;
  const [selections, setSelections] = useState<DisabledSelections>(seed);
  const [pending, setPending] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const serialized = JSON.stringify(seed);
  const pendingRef = useRef(pending);
  pendingRef.current = pending;
  useEffect(() => {
    if (pendingRef.current === null) setSelections(JSON.parse(serialized) as DisabledSelections);
  }, [serialized]);

  const save = useCallback(async (next: DisabledSelections, token: string) => {
    const previous = selections;
    setPending(token);
    setError(null);
    setSelections(next);
    try {
      const response = await fetch("/api/captain/ai/disabled", {
        method: "PUT",
        headers: { Accept: "application/json", "Content-Type": "application/json" },
        body: JSON.stringify(withEveryAxis(next)),
      });
      if (!response.ok) throw new Error((await response.text()).trim() || `Update failed with ${response.status}`);
      setSelections((await response.json()) as DisabledSelections);
      setPending(null);
      await onRefresh();
    } catch (cause) {
      setSelections(previous);
      setError(cause instanceof Error ? cause.message : String(cause));
      setPending(null);
    }
  }, [selections, onRefresh]);

  const setEnabled = useCallback(
    (axis: DisabledAxis, token: string, enabled: boolean) =>
      save({ ...selections, [axis]: toggleToken(selections[axis], token, enabled) }, `${axis}:${token}`),
    [save, selections],
  );
  const setRuntimeEnabled = useCallback(
    (provider: string, mode: string, enabled: boolean) => save({
      ...selections,
      runtimes: toggleRuntime(selections.runtimes, { provider, mode }, enabled),
    }, `runtimes:${provider}:${mode}`),
    [save, selections],
  );
  const setModelEnabled = useCallback((provider: string, id: string, enabled: boolean) => {
    const qualified = `${provider}/${id}`;
    const models = enabled
      ? toggleToken(toggleToken(selections.models, id, true), qualified, true)
      : toggleToken(selections.models, qualified, false);
    return save({ ...selections, models }, `models:${qualified}`);
  }, [save, selections]);

  return {
    selections,
    pending,
    error,
    isOff: (axis, token) => hasToken(selections[axis], token),
    isRuntimeOff: (provider, mode) => hasRuntime(selections.runtimes, { provider, mode }),
    setEnabled,
    setRuntimeEnabled,
    setModelEnabled,
  };
}

function hasToken(tokens: string[] | null | undefined, token: string): boolean {
  const needle = normalize(token);
  return (tokens ?? []).some((candidate) => normalize(candidate) === needle);
}

function hasRuntime(runtimes: DisabledRuntime[] | null | undefined, runtime: DisabledRuntime): boolean {
  return (runtimes ?? []).some((candidate) =>
    normalize(candidate.provider) === normalize(runtime.provider) && normalize(candidate.mode) === normalize(runtime.mode));
}

function toggleToken(tokens: string[] | null | undefined, token: string, enabled: boolean): string[] {
  const kept = (tokens ?? []).filter((candidate) => normalize(candidate) !== normalize(token));
  return enabled ? kept : [...kept, token];
}

function toggleRuntime(
  runtimes: DisabledRuntime[] | null | undefined,
  runtime: DisabledRuntime,
  enabled: boolean,
): DisabledRuntime[] {
  const kept = (runtimes ?? []).filter((candidate) => !hasRuntime([candidate], runtime));
  return enabled ? kept : [...kept, runtime];
}

function normalize(value: string): string {
  return value.trim().toLowerCase();
}

function withEveryAxis(selections: DisabledSelections) {
  return {
    modes: selections.modes ?? [],
    providers: selections.providers ?? [],
    runtimes: selections.runtimes ?? [],
    models: selections.models ?? [],
    efforts: selections.efforts ?? [],
  };
}

import { useCallback, useEffect, useRef, useState } from "react";
import { Switch } from "@flanksource/clicky-ui/components";

/** The persisted opt-out set, mirroring captainconfig.DisabledSelections. */
export type DisabledSelections = {
  modes: string[] | null;
  providers: string[] | null;
  backends: string[] | null;
  models: string[] | null;
  efforts: string[] | null;
};

/** The universe each axis is drawn from, served by whoami so nothing is hardcoded twice. */
export type DisabledAxes = {
  modes: string[];
  providers: string[];
  efforts: string[];
};

export type DisabledAxis = keyof DisabledSelections;

export type DisabledController = {
  selections: DisabledSelections;
  pending: string | null;
  error: string | null;
  /** True when the token appears on its own axis — not the transitive rules the server applies. */
  isOff: (axis: DisabledAxis, token: string) => boolean;
  /** Turn a single token on or off and persist the whole set. */
  setEnabled: (axis: DisabledAxis, token: string, enabled: boolean) => Promise<void>;
  /** Enabling a model has to clear both its qualified and its bare entry. */
  setModelEnabled: (backend: string, id: string, enabled: boolean) => Promise<void>;
};

const EMPTY: DisabledSelections = { modes: null, providers: null, backends: null, models: null, efforts: null };

/**
 * useDisabledSelections owns the whole opt-out set and is the only writer of
 * PUT /api/captain/ai/disabled. The endpoint takes the full set, so every
 * toggle sends the complete state and there is no partially-applied window.
 *
 * State is optimistic: the switch moves immediately, then reconciles against
 * the response, and reverts if the write is rejected.
 */
export function useDisabledSelections(
  initial: DisabledSelections | undefined,
  onRefresh: () => Promise<void>,
): DisabledController {
  const seed = initial ?? EMPTY;
  const [selections, setSelections] = useState<DisabledSelections>(seed);
  const [pending, setPending] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  // A refetch re-seeds the local copy, but only while no write is in flight —
  // otherwise a slow whoami response would snap the switch back mid-toggle.
  const serialized = JSON.stringify(seed);
  const pendingRef = useRef(pending);
  pendingRef.current = pending;
  useEffect(() => {
    if (pendingRef.current === null) setSelections(JSON.parse(serialized) as DisabledSelections);
  }, [serialized]);

  const save = useCallback(
    async (next: DisabledSelections, token: string) => {
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
    },
    [selections, onRefresh],
  );

  const setEnabled = useCallback(
    (axis: DisabledAxis, token: string, enabled: boolean) =>
      save({ ...selections, [axis]: toggleToken(selections[axis], token, enabled) }, `${axis}:${token}`),
    [save, selections],
  );

  const setModelEnabled = useCallback(
    (backend: string, id: string, enabled: boolean) => {
      const qualified = `${backend}/${id}`;
      // Turning a model back on also clears a bare, all-backends entry, which is
      // the only other way it could be off. Turning one off never touches the
      // bare entry: narrowing a global opt-out to one backend is not what the
      // switch says it does.
      const models = enabled
        ? toggleToken(toggleToken(selections.models, id, true), qualified, true)
        : toggleToken(selections.models, qualified, false);
      return save({ ...selections, models }, `models:${qualified}`);
    },
    [save, selections],
  );

  return {
    selections,
    pending,
    error,
    isOff: (axis, token) => hasToken(selections[axis], token),
    setEnabled,
    setModelEnabled,
  };
}

function hasToken(tokens: string[] | null | undefined, token: string) {
  const needle = token.trim().toLowerCase();
  return (tokens ?? []).some((candidate) => candidate.trim().toLowerCase() === needle);
}

function toggleToken(tokens: string[] | null | undefined, token: string, enabled: boolean): string[] {
  const kept = (tokens ?? []).filter((candidate) => candidate.trim().toLowerCase() !== token.trim().toLowerCase());
  return enabled ? kept : [...kept, token];
}

/** The endpoint rejects unknown fields and expects all five axes present. */
function withEveryAxis(selections: DisabledSelections) {
  return {
    modes: selections.modes ?? [],
    providers: selections.providers ?? [],
    backends: selections.backends ?? [],
    models: selections.models ?? [],
    efforts: selections.efforts ?? [],
  };
}

/**
 * DisabledControls renders the three whole-axis rows. Per-backend and per-model
 * switches live on the adapter cards, where the thing being switched off is.
 */
export function DisabledControls({ axes, controller }: { axes: DisabledAxes; controller: DisabledController }) {
  return (
    <section className="grid gap-density-3 rounded-lg border border-border bg-muted/20 p-density-3" aria-labelledby="whoami-disabled">
      <div>
        <h2 id="whoami-disabled" className="text-base font-semibold">Available runtimes</h2>
        <p className="text-xs text-muted-foreground">
          Anything switched off here is dropped from every model picker, prompt schema and fallback chain — not just this page.
        </p>
      </div>
      <AxisRow label="Modes" axis="modes" tokens={axes.modes} controller={controller} />
      <AxisRow label="Providers" axis="providers" tokens={axes.providers} controller={controller} />
      <AxisRow label="Reasoning efforts" axis="efforts" tokens={axes.efforts} controller={controller} />
      {controller.error && <p className="text-xs text-destructive">{controller.error}</p>}
    </section>
  );
}

function AxisRow({
  label,
  axis,
  tokens,
  controller,
}: {
  label: string;
  axis: DisabledAxis;
  tokens: string[];
  controller: DisabledController;
}) {
  if (tokens.length === 0) return null;
  const offCount = tokens.filter((token) => controller.isOff(axis, token)).length;
  // Each switch is named by its visible token, so the row is grouped under its
  // heading to say which axis that token belongs to.
  return (
    <div className="grid gap-density-2" role="group" aria-labelledby={`whoami-axis-${axis}`}>
      <div className="flex items-center gap-density-2">
        <h3 id={`whoami-axis-${axis}`} className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          {label}
        </h3>
        {offCount > 0 && <span className="text-xs text-muted-foreground">{offCount} disabled</span>}
      </div>
      <div className="flex flex-wrap gap-x-density-4 gap-y-density-2">
        {tokens.map((token) => (
          <Switch
            key={token}
            checked={!controller.isOff(axis, token)}
            disabled={controller.pending !== null}
            label={<span className="font-mono text-xs">{token}</span>}
            onChange={(checked) => void controller.setEnabled(axis, token, checked)}
          />
        ))}
      </div>
    </div>
  );
}

import { useId, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Button, Field, Select } from "@flanksource/clicky-ui/components";
import { UiRefresh } from "@flanksource/clicky-ui/icons";
import {
  RuntimeProfilesWorkspace,
  familiesFromRuntimeCatalog,
  type SpecRuntimeFamily,
  type SpecRuntimeSandboxCatalog,
} from "@flanksource/clicky-ui/ai";
import {
  CAPTAIN_SECRET_SELECTOR,
  errorMessage,
  fetchPromptSchema,
} from "./promptWorkbenchApi";
import {
  RUNTIME_DB_TARGET,
  createTargetsFor,
  runtimeProfilesClient,
  runtimeSourcesOf,
  type RuntimeRecordKind,
  type RuntimeRecordSource,
  type StoredRuntimePreset,
} from "./runtimeProfilesApi";
import {
  useRuntimeDrafts,
  useRuntimePresets,
  type RuntimeCreateTargets,
} from "./runtimeProfilesData";
import {
  effectiveRuntimeSelection,
  parseRuntimeProfilesSearch,
  runtimeProfilesLocation,
  type RuntimeProfilesSelection,
} from "./runtimeProfilesRoute";
import { StateMessage } from "./StateMessage";
import { useWhoamiCatalog } from "./whoamiCatalog";

type Navigate = (to: string, opts?: { replace?: boolean }) => void;

export function RuntimeProfilesPage({
  search,
  onNavigate,
}: {
  search: string;
  onNavigate: Navigate;
}) {
  const selection = parseRuntimeProfilesSearch(search);
  const whoami = useWhoamiCatalog();
  const schema = useQuery({
    queryKey: ["prompt-schema"],
    queryFn: fetchPromptSchema,
  });
  const presets = useRuntimePresets();
  const families = useMemo(
    () =>
      whoami.data
        ? familiesFromRuntimeCatalog(whoami.data.runtimes)
        : undefined,
    [whoami.data],
  );
  const loading = whoami.isLoading || schema.isLoading || presets.isLoading;
  const error = whoami.error ?? schema.error ?? presets.error;
  const refetching = presets.isFetching;

  return (
    <div className="h-full overflow-auto">
      <div className="mx-auto flex w-full max-w-[110rem] flex-col gap-density-6 p-density-4 md:p-density-6">
        <header className="flex flex-wrap items-start justify-between gap-density-3">
          <div className="max-w-4xl space-y-density-2">
            <p className="text-xs font-semibold uppercase tracking-wide text-primary">
              Captain · /runtime-presets
            </p>
            <h1 className="text-2xl font-semibold tracking-tight">
              Runtime presets
            </h1>
            <p className="text-sm text-muted-foreground">
              Author complete reusable task specifications and inspect the
              effective spec Captain resolves from an ordered preset selection.
            </p>
          </div>
          <Button
            size="sm"
            variant="outline"
            disabled={refetching}
            onClick={() => void presets.refetch()}
          >
            <UiRefresh className={refetching ? "animate-spin" : undefined} />
            Refresh library
          </Button>
        </header>

        {loading ? (
          <StateMessage>Loading presets...</StateMessage>
        ) : error ? (
          <StateMessage tone="error">{errorMessage(error)}</StateMessage>
        ) : families && schema.data && presets.data ? (
          <RuntimeProfilesLibrary
            selection={selection}
            onNavigate={onNavigate}
            presets={presets.data}
            families={families}
            sources={runtimeSourcesOf(schema.data)}
            sandboxes={schema.data.sandboxes}
          />
        ) : (
          <StateMessage tone="error">
            The runtime library returned no result.
          </StateMessage>
        )}
      </div>
    </div>
  );
}

function RuntimeProfilesLibrary({
  selection,
  onNavigate,
  presets,
  families,
  sources,
  sandboxes,
}: {
  selection: RuntimeProfilesSelection;
  onNavigate: Navigate;
  presets: StoredRuntimePreset[];
  families: SpecRuntimeFamily[];
  sources: RuntimeRecordSource[];
  sandboxes: SpecRuntimeSandboxCatalog | undefined;
}) {
  const [targets, setTargets] = useState(() => defaultCreateTargets(sources));
  const targetId = useId();
  const navigateTo = (next: Partial<RuntimeProfilesSelection>) =>
    onNavigate(runtimeProfilesLocation({ ...selection, ...next }), {
      replace: true,
    });
  const drafts = useRuntimeDrafts({
    presets,
    profiles: [],
    targets,
    onCreatedPreset: (presetId) => navigateTo({ presetId }),
    onCreatedProfile: () => undefined,
  });
  const effective = effectiveRuntimeSelection(selection, drafts);
  const kind: RuntimeRecordKind = "preset";
  const eligible = createTargetsFor(sources, kind);

  return (
    <div className="space-y-density-4">
      {eligible.length > 1 && (
        <Field
          label="Create in"
          htmlFor={targetId}
          labelClassName="text-xs"
          className="max-w-sm"
        >
          <Select
            id={targetId}
            value={targets[kind]}
            onChange={(event) =>
              setTargets({ ...targets, [kind]: event.target.value })
            }
            options={eligible.map((source) => ({
              value: source.id,
              label: source.label,
            }))}
          />
        </Field>
      )}
      <RuntimeProfilesWorkspace
        presets={drafts.presets}
        profiles={[]}
        view="presets"
        onViewChange={() => undefined}
        selectedPresetId={effective.presetId}
        selectedProfileId={effective.profileId}
        onSelectPreset={(presetId) => navigateTo({ presetId })}
        onSelectProfile={() => undefined}
        store={drafts.store}
        client={runtimeProfilesClient}
        families={families}
        secretSelector={CAPTAIN_SECRET_SELECTOR}
        {...(sandboxes ? { sandboxCatalog: sandboxes } : {})}
        persistence={drafts.persistence}
        recordMeta={drafts.recordMeta}
      />
    </div>
  );
}

/** The database when it accepts the kind, else the first eligible source; a kind nobody accepts stays unset. */
function defaultCreateTargets(
  sources: RuntimeRecordSource[],
): RuntimeCreateTargets {
  const targets: RuntimeCreateTargets = {};
  for (const kind of ["preset"] as const) {
    const eligible = createTargetsFor(sources, kind);
    const target =
      eligible.find((source) => source.id === RUNTIME_DB_TARGET) ?? eligible[0];
    if (target) targets[kind] = target.id;
  }
  return targets;
}

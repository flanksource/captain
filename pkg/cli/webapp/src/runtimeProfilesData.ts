import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type {
  ResolvedRuntimeProfile,
  RuntimePreset,
  RuntimeProfile,
  RuntimeProfileResolveRequest,
  RuntimeProfilesPersistence,
  RuntimeProfilesStore,
  RuntimeRecordMeta,
} from "@flanksource/clicky-ui/ai";
import { errorMessage } from "./promptWorkbenchApi";
import {
  RUNTIME_DB_TARGET,
  createRuntimePreset,
  createRuntimeProfile,
  deleteRuntimePreset,
  deleteRuntimeProfile,
  fetchRuntimePresets,
  fetchRuntimeProfiles,
  presetWrite,
  profileWrite,
  resolveRuntimeProfile,
  updateRuntimePreset,
  updateRuntimeProfile,
  type RuntimePresetWrite,
  type RuntimeProfileWrite,
  type RuntimeRecordKind,
  type RuntimeRecordSource,
  type StoredRuntimePreset,
  type StoredRuntimeProfile,
} from "./runtimeProfilesApi";

export const RUNTIME_PRESETS_QUERY_KEY = ["runtime-presets"] as const;
export const RUNTIME_PROFILES_QUERY_KEY = ["runtime-profiles"] as const;

export function useRuntimePresets() {
  return useQuery({
    queryKey: RUNTIME_PRESETS_QUERY_KEY,
    queryFn: fetchRuntimePresets,
    staleTime: 30_000,
  });
}

export function useRuntimeProfiles() {
  return useQuery({
    queryKey: RUNTIME_PROFILES_QUERY_KEY,
    queryFn: fetchRuntimeProfiles,
    staleTime: 30_000,
  });
}

/** The six catalog writes; each one refreshes the lists it can change (deleting a preset can change profile references). */
export function useRuntimeProfileMutations() {
  const queryClient = useQueryClient();
  const refresh = (...keys: Array<readonly string[]>) =>
    Promise.all(keys.map((queryKey) => queryClient.invalidateQueries({ queryKey })));
  const createPreset = useMutation({
    mutationFn: createRuntimePreset,
    onSuccess: () => refresh(RUNTIME_PRESETS_QUERY_KEY),
  });
  const updatePreset = useMutation({
    mutationFn: (write: { id: string; input: RuntimePresetWrite }) =>
      updateRuntimePreset(write.id, write.input),
    onSuccess: () => refresh(RUNTIME_PRESETS_QUERY_KEY),
  });
  const deletePreset = useMutation({
    mutationFn: deleteRuntimePreset,
    onSuccess: () => refresh(RUNTIME_PRESETS_QUERY_KEY, RUNTIME_PROFILES_QUERY_KEY),
  });
  const createProfile = useMutation({
    mutationFn: createRuntimeProfile,
    onSuccess: () => refresh(RUNTIME_PROFILES_QUERY_KEY),
  });
  const updateProfile = useMutation({
    mutationFn: (write: { id: string; input: RuntimeProfileWrite }) =>
      updateRuntimeProfile(write.id, write.input),
    onSuccess: () => refresh(RUNTIME_PROFILES_QUERY_KEY),
  });
  const deleteProfile = useMutation({
    mutationFn: deleteRuntimeProfile,
    onSuccess: () => refresh(RUNTIME_PROFILES_QUERY_KEY),
  });
  return { createPreset, updatePreset, deletePreset, createProfile, updateProfile, deleteProfile };
}

export type PromptRuntimeProfileProps = {
  presets?: RuntimePreset[];
  profiles?: RuntimeProfile[];
  onSaveProfile: (profile: RuntimeProfile) => Promise<RuntimeProfile>;
  onCreateProfile: (profile: RuntimeProfile) => Promise<RuntimeProfile>;
  onResolveProfile: (request: RuntimeProfileResolveRequest) => Promise<ResolvedRuntimeProfile>;
};

/**
 * The profile picker's inputs for the prompt spec editor. The lists are passed
 * only once both loaded; a failed list surfaces through `error` instead of an
 * empty picker that would read as "no profiles". Saves from the picker land
 * in the database.
 */
export function usePromptRuntimeProfiles(): {
  editorProps: PromptRuntimeProfileProps;
  error: unknown;
} {
  const presets = useRuntimePresets();
  const profiles = useRuntimeProfiles();
  const mutations = useRuntimeProfileMutations();
  return {
    error: profiles.error ?? presets.error,
    editorProps: {
      ...(profiles.data && presets.data
        ? { profiles: profiles.data, presets: presets.data }
        : {}),
      onSaveProfile: (profile) =>
        mutations.updateProfile.mutateAsync({ id: profile.id, input: profileWrite(profile) }),
      onCreateProfile: (profile) =>
        mutations.createProfile.mutateAsync({
          target: RUNTIME_DB_TARGET,
          ...profileWrite(profile),
        }),
      onResolveProfile: resolveRuntimeProfile,
    },
  };
}

export function runtimeRecordMeta(record: { source: RuntimeRecordSource }): RuntimeRecordMeta {
  return { sourceLabel: record.source.kind, writable: record.source.writable };
}

/** The source id each record kind is created in; absent when no writable source accepts that kind. */
export type RuntimeCreateTargets = Partial<Record<RuntimeRecordKind, string>>;

export type RuntimeDraftsOptions = {
  presets: StoredRuntimePreset[];
  profiles: StoredRuntimeProfile[];
  targets: RuntimeCreateTargets;
  onCreatedPreset: (id: string) => void;
  onCreatedProfile: (id: string) => void;
};

export type RuntimeDrafts = {
  presets: RuntimePreset[];
  profiles: RuntimeProfile[];
  store: RuntimeProfilesStore;
  persistence: RuntimeProfilesPersistence;
  recordMeta: (id: string) => RuntimeRecordMeta;
};

type Drafts<T> = Record<string, T>;

/**
 * A client-side overlay over the loaded records: the workspace's per-keystroke
 * `update*` calls land here, Save commits every dirty draft with a PUT, and
 * Discard drops them. Create and delete go to the server immediately because
 * the server assigns ids and enforces references.
 */
export function useRuntimeDrafts(options: RuntimeDraftsOptions): RuntimeDrafts {
  const mutations = useRuntimeProfileMutations();
  const [presetDrafts, setPresetDrafts] = useState<Drafts<RuntimePreset>>({});
  const [profileDrafts, setProfileDrafts] = useState<Drafts<RuntimeProfile>>({});
  const [error, setError] = useState<string>();
  const [saving, setSaving] = useState(false);

  const presets = useMemo(
    () => options.presets.map((preset) => presetDrafts[preset.id] ?? preset),
    [options.presets, presetDrafts],
  );
  const profiles = useMemo(
    () => options.profiles.map((profile) => profileDrafts[profile.id] ?? profile),
    [options.profiles, profileDrafts],
  );

  const findRecord = (id: string) => {
    const record =
      options.presets.find((preset) => preset.id === id) ??
      options.profiles.find((profile) => profile.id === id);
    if (!record) throw new Error(`runtime record "${id}" is not loaded`);
    return record;
  };

  const run = async (action: () => Promise<void>) => {
    setError(undefined);
    try {
      await action();
    } catch (cause) {
      setError(errorMessage(cause));
    }
  };

  const targetFor = (kind: RuntimeRecordKind) => {
    const target = options.targets[kind];
    if (!target) throw new Error(`No writable runtime source accepts ${kind}s.`);
    return target;
  };

  const overlay = <T extends { id: string; name: string }>(
    record: T,
    setDrafts: (update: (drafts: Drafts<T>) => Drafts<T>) => void,
  ) => {
    const source = findRecord(record.id).source;
    if (!source.writable) {
      setError(`"${record.name}" lives in ${source.label}, which is read-only`);
      return;
    }
    setDrafts((drafts) => ({ ...drafts, [record.id]: record }));
  };

  const saveAll = async () => {
    setSaving(true);
    try {
      for (const draft of Object.values(presetDrafts)) {
        await mutations.updatePreset.mutateAsync({ id: draft.id, input: presetWrite(draft) });
        setPresetDrafts((drafts) => without(drafts, draft.id));
      }
      for (const draft of Object.values(profileDrafts)) {
        await mutations.updateProfile.mutateAsync({ id: draft.id, input: profileWrite(draft) });
        setProfileDrafts((drafts) => without(drafts, draft.id));
      }
    } finally {
      setSaving(false);
    }
  };

  const store: RuntimeProfilesStore = {
    createPreset: (preset) =>
      void run(async () => {
        const created = await mutations.createPreset.mutateAsync({
          target: targetFor("preset"),
          ...presetWrite(preset),
        });
        options.onCreatedPreset(created.id);
      }),
    updatePreset: (preset) => overlay(preset, setPresetDrafts),
    deletePreset: (id) =>
      void run(async () => {
        await mutations.deletePreset.mutateAsync(id);
        setPresetDrafts((drafts) => without(drafts, id));
      }),
    createProfile: (profile) =>
      void run(async () => {
        const created = await mutations.createProfile.mutateAsync({
          target: targetFor("profile"),
          ...profileWrite(profile),
        });
        options.onCreatedProfile(created.id);
      }),
    updateProfile: (profile) => overlay(profile, setProfileDrafts),
    deleteProfile: (id) =>
      void run(async () => {
        await mutations.deleteProfile.mutateAsync(id);
        setProfileDrafts((drafts) => without(drafts, id));
      }),
  };

  const persistence: RuntimeProfilesPersistence = {
    dirty: Object.keys(presetDrafts).length + Object.keys(profileDrafts).length > 0,
    saving,
    ...(error !== undefined ? { error } : {}),
    onSave: () => void run(saveAll),
    onDiscard: () => {
      setPresetDrafts({});
      setProfileDrafts({});
      setError(undefined);
    },
  };

  return {
    presets,
    profiles,
    store,
    persistence,
    recordMeta: (id) => runtimeRecordMeta(findRecord(id)),
  };
}

function without<T>(drafts: Drafts<T>, id: string): Drafts<T> {
  const next = { ...drafts };
  delete next[id];
  return next;
}

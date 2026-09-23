import type { RuntimeProfilesView } from "@flanksource/clicky-ui/ai";

export const RUNTIME_PROFILES_PATH = "/runtime-presets";

const PRESET_PARAM = "preset";

export type RuntimeProfilesSelection = {
  view: RuntimeProfilesView;
  profileId?: string;
  presetId?: string;
};

/** Reads the selected preset. Legacy profile/view parameters are ignored. */
export function parseRuntimeProfilesSearch(
  search: string,
): RuntimeProfilesSelection {
  const params = new URLSearchParams(search);
  const presetId = params.get(PRESET_PARAM);
  return {
    view: "presets",
    ...(presetId ? { presetId } : {}),
  };
}

/** The query string for a preset selection. Legacy profile/view fields are omitted. */
export function formatRuntimeProfilesSearch(
  selection: RuntimeProfilesSelection,
): string {
  const params = new URLSearchParams();
  if (selection.presetId) params.set(PRESET_PARAM, selection.presetId);
  const search = params.toString();
  return search ? `?${search}` : "";
}

export function runtimeProfilesLocation(
  selection: RuntimeProfilesSelection,
): string {
  return `${RUNTIME_PROFILES_PATH}${formatRuntimeProfilesSearch(selection)}`;
}

/** Fills an unselected preset library with its first record. */
export function effectiveRuntimeSelection(
  selection: RuntimeProfilesSelection,
  records: { presets: Array<{ id: string }>; profiles: Array<{ id: string }> },
): RuntimeProfilesSelection {
  const presetId = selection.presetId ?? records.presets[0]?.id;
  return {
    view: "presets",
    ...(presetId ? { presetId } : {}),
  };
}

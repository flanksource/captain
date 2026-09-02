import type { RuntimeProfilesView } from "@flanksource/clicky-ui/ai";

export const RUNTIME_PROFILES_PATH = "/runtime-profiles";

const VIEW_PARAM = "view";
const PROFILE_PARAM = "profile";
const PRESET_PARAM = "preset";

export type RuntimeProfilesSelection = {
  view: RuntimeProfilesView;
  profileId?: string;
  presetId?: string;
};

/** Reads `?view=presets&profile=…&preset=…`; an unknown view is an error, not a silent default. */
export function parseRuntimeProfilesSearch(search: string): RuntimeProfilesSelection {
  const params = new URLSearchParams(search);
  const view = params.get(VIEW_PARAM);
  if (view !== null && view !== "profiles" && view !== "presets") {
    throw new Error(`runtime profiles view must be "profiles" or "presets", got "${view}"`);
  }
  const profileId = params.get(PROFILE_PARAM);
  const presetId = params.get(PRESET_PARAM);
  return {
    view: view === "presets" ? "presets" : "profiles",
    ...(profileId ? { profileId } : {}),
    ...(presetId ? { presetId } : {}),
  };
}

/** The query string for a selection; the default profiles view carries no `view` param. */
export function formatRuntimeProfilesSearch(selection: RuntimeProfilesSelection): string {
  const params = new URLSearchParams();
  if (selection.view === "presets") params.set(VIEW_PARAM, "presets");
  if (selection.profileId) params.set(PROFILE_PARAM, selection.profileId);
  if (selection.presetId) params.set(PRESET_PARAM, selection.presetId);
  const search = params.toString();
  return search ? `?${search}` : "";
}

export function runtimeProfilesLocation(selection: RuntimeProfilesSelection): string {
  return `${RUNTIME_PROFILES_PATH}${formatRuntimeProfilesSearch(selection)}`;
}

/** Fills an unselected view with its first record so the workspace never opens empty next to a populated list. */
export function effectiveRuntimeSelection(
  selection: RuntimeProfilesSelection,
  records: { presets: Array<{ id: string }>; profiles: Array<{ id: string }> },
): RuntimeProfilesSelection {
  const profileId = selection.profileId ?? records.profiles[0]?.id;
  const presetId = selection.presetId ?? records.presets[0]?.id;
  return {
    view: selection.view,
    ...(profileId ? { profileId } : {}),
    ...(presetId ? { presetId } : {}),
  };
}

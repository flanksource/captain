import { describe, expect, it } from "vitest";
import {
  effectiveRuntimeSelection,
  formatRuntimeProfilesSearch,
  parseRuntimeProfilesSearch,
  runtimeProfilesLocation,
} from "./runtimeProfilesRoute";

describe("parseRuntimeProfilesSearch", () => {
  it("defaults to the profiles view with nothing selected", () => {
    expect(parseRuntimeProfilesSearch("")).toEqual({ view: "profiles" });
  });

  it("reads the presets view and both selections", () => {
    expect(parseRuntimeProfilesSearch("?view=presets&profile=review%2Fplan&preset=org")).toEqual({
      view: "presets",
      profileId: "review/plan",
      presetId: "org",
    });
  });

  it("treats an explicit profiles view like the default", () => {
    expect(parseRuntimeProfilesSearch("?view=profiles")).toEqual({ view: "profiles" });
  });

  it("rejects an unknown view instead of guessing", () => {
    expect(() => parseRuntimeProfilesSearch("?view=quotas")).toThrow(
      'runtime profiles view must be "profiles" or "presets", got "quotas"',
    );
  });

  it("ignores empty selection params", () => {
    expect(parseRuntimeProfilesSearch("?profile=&preset=")).toEqual({ view: "profiles" });
  });
});

describe("formatRuntimeProfilesSearch", () => {
  it("omits the view param for the profiles view", () => {
    expect(formatRuntimeProfilesSearch({ view: "profiles", profileId: "review" })).toBe("?profile=review");
  });

  it("round-trips a full selection", () => {
    const selection = { view: "presets" as const, profileId: "review/plan", presetId: "org" };

    expect(parseRuntimeProfilesSearch(formatRuntimeProfilesSearch(selection))).toEqual(selection);
  });

  it("formats an empty selection as no query string", () => {
    expect(runtimeProfilesLocation({ view: "profiles" })).toBe("/runtime-profiles");
  });
});

describe("effectiveRuntimeSelection", () => {
  const records = { presets: [{ id: "org" }, { id: "plan" }], profiles: [{ id: "review" }] };

  it("selects the first record of each kind when the URL names none", () => {
    expect(effectiveRuntimeSelection({ view: "presets" }, records)).toEqual({
      view: "presets",
      profileId: "review",
      presetId: "org",
    });
  });

  it("keeps an explicit selection", () => {
    expect(effectiveRuntimeSelection({ view: "profiles", presetId: "plan" }, records)).toEqual({
      view: "profiles",
      profileId: "review",
      presetId: "plan",
    });
  });

  it("leaves a kind unselected when it has no records", () => {
    expect(effectiveRuntimeSelection({ view: "profiles" }, { presets: [], profiles: [] })).toEqual({
      view: "profiles",
    });
  });
});

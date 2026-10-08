import { describe, expect, it } from "vitest";
import {
  effectiveRuntimeSelection,
  formatRuntimeProfilesSearch,
  parseRuntimeProfilesSearch,
  runtimeProfilesLocation,
} from "./runtimeProfilesRoute";

describe("parseRuntimeProfilesSearch", () => {
  it("defaults to the presets view with nothing selected", () => {
    expect(parseRuntimeProfilesSearch("")).toEqual({ view: "presets" });
  });

  it("reads the preset selection and ignores legacy profile fields", () => {
    expect(
      parseRuntimeProfilesSearch(
        "?view=presets&profile=review%2Fplan&preset=org",
      ),
    ).toEqual({
      view: "presets",
      presetId: "org",
    });
  });

  it("ignores legacy and unknown view values", () => {
    expect(parseRuntimeProfilesSearch("?view=profiles&profile=review")).toEqual(
      {
        view: "presets",
      },
    );
    expect(parseRuntimeProfilesSearch("?view=quotas")).toEqual({
      view: "presets",
    });
  });

  it("ignores empty selection params", () => {
    expect(parseRuntimeProfilesSearch("?profile=&preset=")).toEqual({
      view: "presets",
    });
  });
});

describe("formatRuntimeProfilesSearch", () => {
  it("omits legacy view and profile fields", () => {
    expect(
      formatRuntimeProfilesSearch({ view: "profiles", profileId: "review" }),
    ).toBe("");
  });

  it("round-trips the preset selection", () => {
    const selection = {
      view: "presets" as const,
      profileId: "review/plan",
      presetId: "org",
    };

    expect(
      parseRuntimeProfilesSearch(formatRuntimeProfilesSearch(selection)),
    ).toEqual({
      view: "presets",
      presetId: "org",
    });
  });

  it("formats an empty selection as no query string", () => {
    expect(runtimeProfilesLocation({ view: "presets" })).toBe(
      "/runtime-presets",
    );
  });
});

describe("effectiveRuntimeSelection", () => {
  const records = {
    presets: [{ id: "org" }, { id: "plan" }],
    profiles: [{ id: "review" }],
  };

  it("selects the first preset when the URL names none", () => {
    expect(effectiveRuntimeSelection({ view: "presets" }, records)).toEqual({
      view: "presets",
      presetId: "org",
    });
  });

  it("keeps an explicit selection", () => {
    expect(
      effectiveRuntimeSelection(
        { view: "profiles", profileId: "review", presetId: "plan" },
        records,
      ),
    ).toEqual({
      view: "presets",
      presetId: "plan",
    });
  });

  it("leaves a kind unselected when it has no records", () => {
    expect(
      effectiveRuntimeSelection(
        { view: "profiles" },
        { presets: [], profiles: [] },
      ),
    ).toEqual({
      view: "presets",
    });
  });
});

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createRuntimePreset,
  createTargetsFor,
  deleteRuntimePreset,
  fetchRuntimePresets,
  fetchRuntimeProfileResolution,
  fetchRuntimeProfiles,
  resolveRuntimeProfile,
  runtimeProfilesClient,
  runtimeSourcesOf,
  updateRuntimeProfile,
  type RuntimeRecordSource,
  type StoredRuntimePreset,
  type StoredRuntimeProfile,
} from "./runtimeProfilesApi";

const DB_SOURCE: RuntimeRecordSource = {
  kind: "db",
  id: "db",
  label: "captain database",
  writable: true,
  records: ["preset", "profile"],
};

const PRESET: StoredRuntimePreset = {
  id: "preset-1",
  key: "organization-defaults",
  name: "Organization defaults",
  scope: "global",
  spec: { model: "anthropic/claude-sonnet-5", mode: "cli" },
  source: DB_SOURCE,
  updatedAt: "2026-09-01T10:00:00Z",
};

const PROFILE: StoredRuntimeProfile = {
  id: "profile-1",
  key: "review",
  name: "Review",
  description: "Plan-mode review",
  spec: { mode: "cli" },
  presets: [PRESET.id],
  source: DB_SOURCE,
  updatedAt: "2026-09-01T10:00:00Z",
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("runtime record fetchers", () => {
  it("returns the bare array of records without the entity list's leading _id", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse([{ _id: PRESET.id, ...PRESET }])));

    await expect(fetchRuntimePresets()).resolves.toStrictEqual([PRESET]);
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/runtime-preset",
      expect.objectContaining({ headers: { Accept: "application/json" } }),
    );
  });

  it("rejects a list that is not an array, naming the URL", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ items: [PRESET] })));

    await expect(fetchRuntimeProfiles()).rejects.toThrow(
      "/api/v1/runtime-profile must return a JSON array of runtime records",
    );
  });

  it.each([
    ["id", { ...PRESET, id: undefined }],
    ["name", { ...PRESET, name: 7 }],
    ["spec", { ...PRESET, spec: "cli" }],
    ["source", { ...PRESET, source: { kind: "db" } }],
    ["source.records", { ...PRESET, source: { ...DB_SOURCE, records: ["prompt"] } }],
  ])("rejects a record without a valid %s, naming the URL and index", async (_field, record) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse([PRESET, record])));

    await expect(fetchRuntimePresets()).rejects.toThrow(
      "/api/v1/runtime-preset[1] must be a runtime record with id, name, spec and source",
    );
  });

  it("surfaces the server error text for a failed list", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("404 page not found", { status: 404 })));

    await expect(fetchRuntimePresets()).rejects.toThrow("404 page not found");
  });
});

describe("createRuntimePreset", () => {
  it("posts the record with its create target and returns the stored record", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(PRESET));
    vi.stubGlobal("fetch", fetchMock);

    const created = await createRuntimePreset({
      target: "file:user",
      name: "Organization defaults",
      scope: "global",
      spec: { mode: "cli" },
    });

    expect(created).toEqual(PRESET);
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/runtime-preset",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          target: "file:user",
          name: "Organization defaults",
          scope: "global",
          spec: { mode: "cli" },
        }),
      }),
    );
  });
});

describe("updateRuntimeProfile", () => {
  it("puts to the collection URL with the id in the body", async () => {
    const stored = { ...PRESET, presets: ["preset-1"] };
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(stored));
    vi.stubGlobal("fetch", fetchMock);

    await expect(
      updateRuntimeProfile("file:user/review", { name: "Review", spec: {}, presets: ["preset-1"] }),
    ).resolves.toEqual(stored);
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/runtime-profile",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({ id: "file:user/review", name: "Review", spec: {}, presets: ["preset-1"] }),
      }),
    );
  });
});

describe("runtime sources", () => {
  const FILE_SOURCE: RuntimeRecordSource = {
    kind: "file",
    id: "file:user",
    label: "~/.config/captain",
    writable: true,
    records: ["preset"],
  };
  const READ_ONLY: RuntimeRecordSource = { ...FILE_SOURCE, id: "file:embedded", writable: false, records: ["preset", "profile"] };

  it("reads runtimeSources from the prompt schema document", () => {
    expect(runtimeSourcesOf({ schemaVersion: 1, sources: [], runtimeSources: [DB_SOURCE, FILE_SOURCE] } as never)).toEqual([
      DB_SOURCE,
      FILE_SOURCE,
    ]);
  });

  it("rejects a schema document without runtimeSources", () => {
    expect(() => runtimeSourcesOf({ schemaVersion: 1, sources: [] })).toThrow(
      "Prompt schema document has no runtimeSources list",
    );
  });

  it("offers only writable sources that accept the record kind as create targets", () => {
    const sources = [DB_SOURCE, FILE_SOURCE, READ_ONLY];

    expect(createTargetsFor(sources, "preset").map((source) => source.id)).toEqual(["db", "file:user"]);
    expect(createTargetsFor(sources, "profile").map((source) => source.id)).toEqual(["db"]);
  });
});

describe("deleteRuntimePreset", () => {
  it("surfaces the 409 text naming the referencing profiles", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response('preset "Organization defaults" is used by profiles: Plan and review', {
          status: 409,
        }),
      ),
    );

    await expect(deleteRuntimePreset("preset-1")).rejects.toThrow(
      'preset "Organization defaults" is used by profiles: Plan and review',
    );
  });

  it("encodes the id in the path", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await deleteRuntimePreset("file:user/plan mode");

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/runtime-preset/file%3Auser%2Fplan%20mode",
      expect.objectContaining({ method: "DELETE" }),
    );
  });
});

describe("fetchRuntimeProfileResolution", () => {
  it("reads the entity resolve action", async () => {
    const resolution = {
      profile: { ...PRESET, presets: ["preset-1"] },
      presets: [PRESET],
      resolved: { spec: { mode: "cli" }, constraints: {}, trace: [] },
    };
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(resolution));
    vi.stubGlobal("fetch", fetchMock);

    await expect(fetchRuntimeProfileResolution("Review")).resolves.toEqual(resolution);
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/runtime-profile/Review/resolve",
      expect.anything(),
    );
  });

  it("rejects a payload without the resolved spec", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ profile: {}, presets: [] })));

    await expect(fetchRuntimeProfileResolution("Review")).rejects.toThrow(
      "/api/v1/runtime-profile/Review/resolve must return profile, presets and resolved",
    );
  });
});

describe("runtimeProfilesClient", () => {
  const RESOLVED = {
    resolved: { spec: { mode: "cli" }, constraints: {}, trace: [] },
    tools: [],
    permissions: {},
    permissionSupport: {},
    effectivePolicy: [],
  };

  it("resolves drafts through the chat resolve endpoint with the abort signal", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(RESOLVED));
    vi.stubGlobal("fetch", fetchMock);
    const request = {
      profile: { id: "p", name: "Review", spec: {}, presets: [] },
      presets: [],
    };
    const controller = new AbortController();

    await expect(runtimeProfilesClient.resolve(request, controller.signal)).resolves.toEqual(RESOLVED);
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/chat/runtime-profiles/resolve",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify(request),
        signal: controller.signal,
      }),
    );
  });

  it("sends only the contract fields of catalog records, dropping key, source, updatedAt and _id", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(RESOLVED));
    vi.stubGlobal("fetch", fetchMock);

    await resolveRuntimeProfile({
      profile: { _id: PROFILE.id, ...PROFILE },
      presets: [{ _id: PRESET.id, ...PRESET }],
    });

    expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body))).toEqual({
      profile: {
        id: PROFILE.id,
        name: PROFILE.name,
        description: PROFILE.description,
        spec: PROFILE.spec,
        presets: PROFILE.presets,
      },
      presets: [{ id: PRESET.id, name: PRESET.name, scope: PRESET.scope, spec: PRESET.spec }],
    });
  });

  it("rejects a resolve payload without tools and permissions", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ resolved: { spec: {}, trace: [] } })));

    await expect(
      resolveRuntimeProfile({ profile: { id: "p", name: "Review", spec: {}, presets: [] }, presets: [] }),
    ).rejects.toThrow("/api/chat/runtime-profiles/resolve must return resolved, tools and permissions");
  });

  it("loads the permission catalog for a provider and mode", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ tools: [] }));
    vi.stubGlobal("fetch", fetchMock);

    await runtimeProfilesClient.loadPermissionCatalog({ provider: "anthropic", mode: "cli" });

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/captain/ai/permissions/catalog?provider=anthropic&mode=cli",
      expect.anything(),
    );
  });
});

function jsonResponse(value: unknown) {
  return new Response(JSON.stringify(value), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

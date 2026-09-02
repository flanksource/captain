import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { RuntimeProfilesPage } from "./RuntimeProfilesPage";

const DB_SOURCE = {
  kind: "db",
  id: "db",
  label: "captain database",
  writable: true,
  records: ["preset", "profile"],
};
const FILE_SOURCE = {
  kind: "file",
  id: "file:user",
  label: "~/.config/captain",
  root: "/home/example/.config/captain",
  writable: true,
  implicit: true,
  records: ["preset"],
};

const WHOAMI = {
  defaultProvider: "anthropic",
  models: [],
  adapters: [],
  runtimes: [
    {
      family: "claude",
      provider: "anthropic",
      catalogPrefix: "anthropic",
      modes: [{ mode: "cli", kind: "cli", keyless: true }],
    },
  ],
};

const SCHEMA = { schemaVersion: 1, sources: [], runtimeSources: [DB_SOURCE, FILE_SOURCE] };

const PRESETS = [
  {
    id: "organization-defaults",
    key: "organization-defaults",
    name: "Organization defaults",
    scope: "global",
    spec: { model: "anthropic/claude-sonnet-5", mode: "cli" },
    source: DB_SOURCE,
    updatedAt: "2026-09-01T10:00:00Z",
  },
  {
    id: "plan-mode",
    key: "plan-mode",
    name: "Plan mode",
    scope: "surface",
    spec: { mode: "cli" },
    source: FILE_SOURCE,
    updatedAt: "2026-09-01T10:00:00Z",
  },
];

const PROFILES = [
  {
    id: "review-profile",
    key: "review",
    name: "Plan and review",
    description: "Read-only planning",
    spec: {},
    presets: ["organization-defaults"],
    source: FILE_SOURCE,
    updatedAt: "2026-09-01T10:00:00Z",
  },
  {
    id: "coding-profile",
    key: "coding",
    name: "Autonomous coding",
    spec: {},
    presets: [],
    source: DB_SOURCE,
    updatedAt: "2026-09-01T10:00:00Z",
  },
];

const RESOLUTION = {
  resolved: {
    spec: { model: "anthropic/claude-sonnet-5", mode: "cli" },
    constraints: {},
    trace: [
      {
        id: "organization-defaults",
        name: "Organization defaults",
        scope: "global",
        source: "preset",
        spec: {},
        constraints: {},
      },
    ],
  },
  tools: [],
  permissions: {},
  permissionSupport: {},
  effectivePolicy: [],
};

type Call = { method: string; url: string; body?: unknown };
type Route = (call: Call) => Response;

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("RuntimeProfilesPage", () => {
  it("selects the profile named in the URL and badges where each record lives", async () => {
    stubLibrary();
    renderPage("?profile=coding-profile");

    expect(await screen.findByLabelText("Profile name")).toHaveValue("Autonomous coding");
    expect(screen.getAllByText("file").length).toBeGreaterThan(0);
    expect(screen.getAllByText("db").length).toBeGreaterThan(0);
    await waitFor(() => expect(screen.getByText("Resolved")).toBeInTheDocument());
    expect(within(screen.getByRole("list", { name: "Resolution order" })).getByText("Organization defaults")).toBeInTheDocument();
  });

  it("creates a profile in the database by default and navigates to the stored id", async () => {
    const calls = stubLibrary({
      "POST /api/v1/runtime-profile": (call) =>
        jsonResponse({ ...(call.body as object), id: "profile-new", key: "new-profile", source: DB_SOURCE, updatedAt: "" }),
    });
    const onNavigate = vi.fn();
    renderPage("", onNavigate);

    fireEvent.click(await screen.findByRole("button", { name: "Create Profiles" }));

    expect(screen.queryByLabelText("Create in")).not.toBeInTheDocument();
    await waitFor(() =>
      expect(onNavigate).toHaveBeenLastCalledWith("/runtime-profiles?profile=profile-new", { replace: true }),
    );
    expect(calls.find((call) => call.method === "POST" && call.url === "/api/v1/runtime-profile")?.body).toEqual({
      target: "db",
      name: "New profile",
      spec: {},
      presets: [],
    });
  });

  it("creates in the source picked in the create target, offered only for sources holding that kind", async () => {
    const calls = stubLibrary({
      "POST /api/v1/runtime-preset": (call) =>
        jsonResponse({ ...(call.body as object), id: "preset-new", key: "new-preset", source: FILE_SOURCE, updatedAt: "" }),
    });
    const onNavigate = vi.fn();
    renderPage("?view=presets", onNavigate);

    const createIn = await screen.findByLabelText("Create in");
    expect(within(createIn).getAllByRole("option").map((option) => option.textContent)).toEqual([
      "captain database",
      "~/.config/captain",
    ]);
    fireEvent.change(createIn, { target: { value: "file:user" } });
    fireEvent.click(screen.getByRole("button", { name: "Create Presets" }));

    await waitFor(() =>
      expect(onNavigate).toHaveBeenLastCalledWith("/runtime-profiles?view=presets&preset=preset-new", { replace: true }),
    );
    expect(calls.find((call) => call.method === "POST" && call.url === "/api/v1/runtime-preset")?.body).toMatchObject({
      target: "file:user",
      name: "New preset",
      scope: "surface",
    });
  });

  it("overlays edits as a draft and commits them with a PUT on Save", async () => {
    const calls = stubLibrary({
      "PUT /api/v1/runtime-profile": (call) => jsonResponse({ ...PROFILES[0], ...(call.body as object) }),
    });
    renderPage("?profile=review-profile");

    fireEvent.change(await screen.findByLabelText("Profile name"), { target: { value: "Plan, then review" } });

    const bar = screen.getByRole("group", { name: "Persistence" });
    expect(within(bar).getByText("Unsaved changes")).toBeInTheDocument();
    expect(calls.filter((call) => call.method === "PUT")).toHaveLength(0);

    fireEvent.click(within(bar).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(within(bar).getByText("All changes saved")).toBeInTheDocument());
    expect(calls.find((call) => call.method === "PUT")).toEqual({
      method: "PUT",
      url: "/api/v1/runtime-profile",
      body: {
        id: "review-profile",
        name: "Plan, then review",
        description: "Read-only planning",
        spec: {},
        presets: ["organization-defaults"],
      },
    });
  });

  it("keeps the draft and reports the server error when Save fails", async () => {
    stubLibrary({
      "PUT /api/v1/runtime-profile": () =>
        new Response('runtime profile name "Autonomous coding" is already taken', { status: 409 }),
    });
    renderPage("?profile=review-profile");

    fireEvent.change(await screen.findByLabelText("Profile name"), { target: { value: "Autonomous coding" } });
    const bar = screen.getByRole("group", { name: "Persistence" });
    fireEvent.click(within(bar).getByRole("button", { name: "Save" }));

    expect(await within(bar).findByRole("alert")).toHaveTextContent(
      'runtime profile name "Autonomous coding" is already taken',
    );
    expect(within(bar).getByText("Unsaved changes")).toBeInTheDocument();
    expect(screen.getByLabelText("Profile name")).toHaveValue("Autonomous coding");
  });

  it("opens the presets view on the preset named in the URL and blocks deleting a referenced preset", async () => {
    stubLibrary();
    renderPage("?view=presets&preset=plan-mode");

    expect(await screen.findByLabelText("Preset name")).toHaveValue("Plan mode");
    const referenced = screen.getByRole("button", { name: "Delete Organization defaults" });
    expect(referenced).toBeDisabled();
    expect(referenced).toHaveAttribute("title", "Used by Plan and review");
    expect(screen.getByRole("button", { name: "Delete Plan mode" })).toBeEnabled();
  });

  it("deletes an unreferenced preset on the server and moves the selection", async () => {
    const calls = stubLibrary({
      "DELETE /api/v1/runtime-preset/plan-mode": () => new Response(null, { status: 204 }),
    });
    const onNavigate = vi.fn();
    renderPage("?view=presets&preset=plan-mode", onNavigate);

    fireEvent.click(await screen.findByRole("button", { name: "Delete Plan mode" }));

    await waitFor(() =>
      expect(calls.find((call) => call.method === "DELETE")?.url).toBe("/api/v1/runtime-preset/plan-mode"),
    );
    expect(onNavigate).toHaveBeenCalledWith(
      "/runtime-profiles?view=presets&preset=organization-defaults",
      { replace: true },
    );
  });

  it("shows the list error when the server does not serve runtime entities", async () => {
    stubLibrary({
      "GET /api/v1/runtime-preset": () => new Response("404 page not found", { status: 404 }),
    });
    renderPage("");

    expect(await screen.findByText("404 page not found")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create Profiles" })).not.toBeInTheDocument();
  });
});

function stubLibrary(extra: Record<string, Route> = {}) {
  const routes: Record<string, Route> = {
    "POST /api/v1/whoami?models=true&limit=0&disabled=true": () => jsonResponse(WHOAMI),
    "GET /api/captain/ai/prompt/schema": () => jsonResponse(SCHEMA),
    "GET /api/v1/runtime-preset": () => jsonResponse(PRESETS),
    "GET /api/v1/runtime-profile": () => jsonResponse(PROFILES),
    "POST /api/chat/runtime-profiles/resolve": () => jsonResponse(RESOLUTION),
    "GET /api/captain/ai/permissions/catalog?provider=anthropic&mode=cli": () => jsonResponse({ tools: [] }),
    ...extra,
  };
  const calls: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      const call: Call = {
        method: init?.method ?? "GET",
        url: String(input),
        ...(init?.body ? { body: JSON.parse(String(init.body)) } : {}),
      };
      calls.push(call);
      const route = routes[`${call.method} ${call.url}`];
      if (!route) return new Response(`no stub for ${call.method} ${call.url}`, { status: 599 });
      return route(call);
    }),
  );
  return calls;
}

function jsonResponse(value: unknown) {
  return new Response(JSON.stringify(value), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function renderPage(search: string, onNavigate = vi.fn()) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <RuntimeProfilesPage search={search} onNavigate={onNavigate} />
    </QueryClientProvider>,
  );
}

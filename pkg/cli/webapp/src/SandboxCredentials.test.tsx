import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SandboxCredentials } from "./SandboxCredentials";

const VIEW = {
  config: {
    refreshMargin: "1h",
    publish: [{ namespace: "captain", secret: "", providers: ["claude"] }],
  },
  status: [
    {
      provider: "claude",
      source: "keychain",
      key: "oauth",
      expiresAt: "2026-08-25T00:00:00Z",
      expiresIn: "6d",
      expired: false,
      targets: ["secret captain/captain-agent-credentials"],
    },
    // A provider that cannot be read is a row with a reason, not a missing row.
    {
      provider: "codex",
      source: "not logged in",
      key: "",
      expiresAt: "0001-01-01T00:00:00Z",
      expiresIn: "",
      expired: true,
    },
  ],
  providers: ["claude", "codex"],
  defaultSecret: "captain-agent-credentials",
  defaultMargin: "5m0s",
};

function stubFetch(overrides: Record<string, unknown> = {}) {
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const ok = (body: unknown) =>
      Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve(body),
        text: () => Promise.resolve(JSON.stringify(body)),
      });
    if (url.includes("/sandbox/credentials/config")) {
      return ok(JSON.parse(String(init?.body ?? "{}")));
    }
    if (url.includes("/sandbox/credentials/sync")) {
      return ok(overrides.sync ?? { published: ["claude"], targets: ["secret captain/x"] });
    }
    if (url.includes("/sandbox/credentials")) return ok(overrides.view ?? VIEW);
    throw new Error(`unexpected fetch: ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderPanel() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <SandboxCredentials />
    </QueryClientProvider>,
  );
}

/** The PUT body, which is the contract with ~/.captain.yaml. */
function savedConfig(fetchMock: ReturnType<typeof stubFetch>) {
  const call = fetchMock.mock.calls.find(
    ([url, init]) =>
      String(url).includes("/credentials/config") &&
      (init as RequestInit | undefined)?.method === "PUT",
  );
  if (!call) throw new Error("no config was saved");
  return JSON.parse(String((call[1] as RequestInit).body)) as Record<string, unknown>;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("SandboxCredentials", () => {
  // A sidecar with no credential enrolls, goes ready, and fails its first task,
  // so which login expires when is the panel's whole reason to exist.
  it("reports each login's expiry and where it publishes", async () => {
    stubFetch();
    renderPanel();

    await screen.findByText("claude");
    expect(screen.getByText(/expires in 6d/)).toBeInTheDocument();
    expect(screen.getByText(/secret captain\/captain-agent-credentials/)).toBeInTheDocument();
    // A login that cannot be read is stated rather than omitted.
    expect(screen.getByText("codex")).toBeInTheDocument();
    expect(screen.getByText("expired")).toBeInTheDocument();
  });

  it("saves an edited refresh margin as a duration string", async () => {
    const fetchMock = stubFetch();
    renderPanel();

    const margin = await screen.findByDisplayValue("1h");
    fireEvent.change(margin, { target: { value: "90m" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(savedConfig(fetchMock).refreshMargin).toBe("90m"));
  });

  // Empty is the documented way to turn mirroring off, so removing the last
  // destination has to reach the server as an empty list rather than as nothing.
  it("sends an empty publish list when the last destination is removed", async () => {
    const fetchMock = stubFetch();
    renderPanel();

    fireEvent.click(await screen.findByRole("button", { name: "Remove" }));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(savedConfig(fetchMock).publish).toEqual([]));
  });

  // A directory and a Secret are mutually exclusive server-side, so the row an
  // operator adds has to commit to one of them.
  it("adds a directory destination without a namespace", async () => {
    const fetchMock = stubFetch();
    renderPanel();

    fireEvent.click(await screen.findByRole("button", { name: "Add directory" }));
    fireEvent.change(screen.getByPlaceholderText("~/.captain/credentials"), {
      target: { value: "/srv/creds" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      const publish = savedConfig(fetchMock).publish as Array<Record<string, unknown>>;
      expect(publish[1]).toMatchObject({ directory: "/srv/creds" });
      expect(publish[1]).not.toHaveProperty("namespace");
    });
  });

  it("publishes once and reports what it wrote", async () => {
    const fetchMock = stubFetch();
    renderPanel();

    // The button renders immediately but stays disabled until the panel knows
    // what it would publish, so clicking before then does nothing.
    await screen.findByText("claude");
    fireEvent.click(screen.getByRole("button", { name: "Sync now" }));

    await screen.findByText(/Published claude/);
    // The saved destinations are used, so the body carries no override.
    const call = fetchMock.mock.calls.find(([url]) =>
      String(url).includes("/credentials/sync"),
    );
    expect(JSON.parse(String((call?.[1] as RequestInit)?.body))).toEqual({});
  });

  // No destinations means nothing is mirrored, which looks identical to a
  // working setup until an agent fails its first task.
  it("says plainly when nothing is mirrored", async () => {
    stubFetch({ view: { ...VIEW, config: { refreshMargin: "", publish: [] } } });
    renderPanel();

    await screen.findByText(/No destinations, so nothing is mirrored/);
  });
});

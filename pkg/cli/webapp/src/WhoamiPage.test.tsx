import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { WhoamiPage } from "./WhoamiPage";

const AXES = {
  modes: ["api", "cli", "agent", "cmux"],
  providers: ["anthropic", "openai", "gemini", "deepseek"],
  efforts: ["low", "medium", "high", "xhigh", "max", "ultra"],
};

const NOTHING_DISABLED = {
  modes: null,
  providers: null,
  backends: null,
  models: null,
  efforts: null,
};

const WHOAMI_RESULT = {
  defaultProvider: "gemini",
  axes: AXES,
  disabled: NOTHING_DISABLED,
  providerDefaults: {
    gemini: {
      agent: "gemini-cli",
      model: "gemini-3.5-flash",
      effort: "high",
      configured: true,
    },
  },
  adapters: [
    {
      backend: "gemini",
      type: "api",
      provider: "gemini",
      mode: "api",
      authenticated: false,
      modelCount: 0,
      modelError: "set GEMINI_API_KEY or GOOGLE_API_KEY to list models",
    },
    {
      backend: "gemini-cli",
      type: "cli",
      provider: "gemini",
      mode: "cli",
      authenticated: true,
      authMethod: "gemini login",
      authDetail: "/home/example/.gemini/oauth_creds.json",
      binary: "/usr/local/bin/gemini",
      modelCount: 2,
      models: ["gemini-3.5-flash", "gemini-2.5-pro"],
      modelDetails: [
        {
          id: "gemini-3.5-flash",
          label: "Gemini 3.5 Flash",
          backend: "gemini-cli",
          releaseDate: "2026-05-19",
          reasoning: true,
          temperature: true,
          supportedEfforts: ["low", "medium", "high"],
        },
        {
          id: "gemini-2.5-pro",
          label: "Gemini 2.5 Pro",
          backend: "gemini-cli",
          releaseDate: "2025-06-17",
          reasoning: true,
          temperature: true,
        },
      ],
    },
  ],
};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("WhoamiPage", () => {
  it("renders the complete whoami adapter and model details", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify(WHOAMI_RESULT), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    renderWhoamiPage();

    expect(screen.getByRole("heading", { name: "AI adapters" })).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "API providers" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "CLI agents" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Configure API token" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Provider defaults" })).toBeInTheDocument();
    expect(screen.getByText("Active default")).toBeInTheDocument();
    expect(screen.getAllByLabelText(/API token$/)).toHaveLength(1);
    expect(screen.getByText("gemini", { selector: "h3" })).toBeInTheDocument();
    expect(screen.getByText("Needs setup")).toBeInTheDocument();
    expect(
      screen.getByText("set GEMINI_API_KEY or GOOGLE_API_KEY to list models"),
    ).toBeInTheDocument();
    expect(screen.getByText("gemini login")).toBeInTheDocument();
    expect(screen.getByText("/home/example/.gemini/oauth_creds.json")).toBeInTheDocument();
    expect(screen.getByText("/usr/local/bin/gemini")).toBeInTheDocument();
    expect(screen.getByText("Gemini 3.5 Flash", { selector: "div" })).toBeInTheDocument();
    expect(screen.getByText("gemini-3.5-flash")).toBeInTheDocument();
    expect(screen.getByText("2026-05-19")).toBeInTheDocument();
    expect(screen.getByText("low / medium / high")).toBeInTheDocument();
    expect(screen.getByText("Gemini 2.5 Pro", { selector: "div" })).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/whoami?models=true&limit=0",
      expect.objectContaining({ method: "POST", body: "{}" }),
    );
  });

  it("surfaces a failed whoami request", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response("probe failed", { status: 500 })),
    );

    renderWhoamiPage();

    expect(await screen.findByText("probe failed")).toBeInTheDocument();
  });

  it("validates and saves an API token, clears it, and refreshes whoami", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(WHOAMI_RESULT))
      .mockResolvedValueOnce(jsonResponse({
        provider: "gemini",
        valid: true,
        saved: true,
        source: "captain-vault",
        maskedToken: "gemi…cret",
        modelCount: 1,
      }))
      .mockResolvedValueOnce(jsonResponse(WHOAMI_RESULT));
    vi.stubGlobal("fetch", fetchMock);
    renderWhoamiPage();

    const input = await screen.findByLabelText("gemini API token");
    fireEvent.change(input, { target: { value: "gemini-provider-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Save & test gemini token" }));

    expect(await screen.findByText("Token saved and validated against 1 model.")).toBeInTheDocument();
    expect(input).toHaveValue("");
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      "/api/captain/ai/providers/gemini/token",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({ token: "gemini-provider-secret" }),
      }),
    );
  });

  it("retains a rejected token and shows the provider error", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(WHOAMI_RESULT))
      .mockResolvedValueOnce(new Response("validate gemini credential: HTTP 401", { status: 422 }));
    vi.stubGlobal("fetch", fetchMock);
    renderWhoamiPage();

    const input = await screen.findByLabelText("gemini API token");
    fireEvent.change(input, { target: { value: "rejected-provider-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Save & test gemini token" }));

    expect(await screen.findByText("validate gemini credential: HTTP 401")).toBeInTheDocument();
    expect(input).toHaveValue("rejected-provider-secret");
  });

  it("tests the current API credential without sending a token", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(WHOAMI_RESULT))
      .mockResolvedValueOnce(jsonResponse({
        provider: "gemini",
        valid: true,
        saved: false,
        source: "environment",
        maskedToken: "gemi…cret",
        modelCount: 2,
      }));
    vi.stubGlobal("fetch", fetchMock);
    renderWhoamiPage();

    fireEvent.click(await screen.findByRole("button", { name: "Test current gemini token" }));

    expect(await screen.findByText("Current token is valid for 2 models.")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      "/api/captain/ai/providers/gemini/token/test",
      expect.objectContaining({ method: "POST", body: "{}" }),
    );
  });

  it("saves model, effort, and agent defaults for the provider", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(WHOAMI_RESULT))
      .mockResolvedValueOnce(jsonResponse({
        provider: "gemini",
        agent: "gemini-cli",
        model: "gemini-2.5-pro",
        effort: "",
        active: true,
      }))
      .mockResolvedValueOnce(jsonResponse(WHOAMI_RESULT));
    vi.stubGlobal("fetch", fetchMock);
    renderWhoamiPage();

    fireEvent.change(await screen.findByLabelText("gemini default model"), {
      target: { value: "gemini-2.5-pro" },
    });
    fireEvent.change(screen.getByLabelText("gemini default effort"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Save defaults" }));

    expect(await screen.findByText("Provider defaults saved.")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      "/api/captain/ai/providers/gemini/defaults",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({ agent: "gemini-cli", model: "gemini-2.5-pro", effort: "" }),
      }),
    );
  });

  it("sets a provider as the active default", async () => {
    const inactive = { ...WHOAMI_RESULT, defaultProvider: "anthropic" };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(inactive))
      .mockResolvedValueOnce(jsonResponse({ provider: "gemini" }))
      .mockResolvedValueOnce(jsonResponse(WHOAMI_RESULT));
    vi.stubGlobal("fetch", fetchMock);
    renderWhoamiPage();

    fireEvent.click(await screen.findByRole("button", { name: "Set as default" }));

    expect(await screen.findByText("Active default")).toBeInTheDocument();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      "/api/captain/ai/default-provider",
      expect.objectContaining({ method: "PUT", body: JSON.stringify({ provider: "gemini" }) }),
    );
  });

  it("sends the whole opt-out set when a mode is switched off, then refetches", async () => {
    const saved = { ...NOTHING_DISABLED, modes: ["cmux"] };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(WHOAMI_RESULT))
      .mockResolvedValueOnce(jsonResponse(saved))
      .mockResolvedValueOnce(jsonResponse({ ...WHOAMI_RESULT, disabled: saved }));
    vi.stubGlobal("fetch", fetchMock);
    renderWhoamiPage();

    fireEvent.click(await screen.findByRole("switch", { name: "cmux" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      "/api/captain/ai/disabled",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({
          modes: ["cmux"],
          providers: [],
          backends: [],
          models: [],
          efforts: [],
        }),
      }),
    );
    expect(screen.getByRole("switch", { name: "cmux" })).toHaveAttribute("aria-checked", "false");
  });

  it("keeps its own switch on when a write is rejected and shows the reason", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(WHOAMI_RESULT))
      .mockResolvedValueOnce(new Response("cannot disable every reasoning effort", { status: 422 }));
    vi.stubGlobal("fetch", fetchMock);
    renderWhoamiPage();

    fireEvent.click(await screen.findByRole("switch", { name: "ultra" }));

    expect(await screen.findByText("cannot disable every reasoning effort")).toBeInTheDocument();
    expect(screen.getByRole("switch", { name: "ultra" })).toHaveAttribute("aria-checked", "true");
  });

  it("writes a per-model opt-out qualified by its backend", async () => {
    const saved = { ...NOTHING_DISABLED, models: ["gemini-cli/gemini-2.5-pro"] };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(WHOAMI_RESULT))
      .mockResolvedValueOnce(jsonResponse(saved))
      .mockResolvedValueOnce(jsonResponse({ ...WHOAMI_RESULT, disabled: saved }));
    vi.stubGlobal("fetch", fetchMock);
    renderWhoamiPage();

    fireEvent.click(await screen.findByRole("switch", { name: "Enable gemini-cli/gemini-2.5-pro" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      "/api/captain/ai/disabled",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({
          modes: [],
          providers: [],
          backends: [],
          models: ["gemini-cli/gemini-2.5-pro"],
          efforts: [],
        }),
      }),
    );
  });

  it("shows a card disabled by its provider as read-only, naming the axis that did it", async () => {
    const disabledByProvider = {
      ...WHOAMI_RESULT,
      disabled: { ...NOTHING_DISABLED, providers: ["gemini"] },
      adapters: WHOAMI_RESULT.adapters.map((adapter) => ({
        ...adapter,
        disabled: true,
        disabledReason: "provider gemini",
        modelDetails: adapter.modelDetails?.map((model) => ({ ...model, disabled: true })),
      })),
    };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(disabledByProvider)));
    renderWhoamiPage();

    const card = await screen.findByRole("switch", { name: "Enable gemini-cli" });
    expect(card).toBeDisabled();
    expect(card).toHaveAttribute("aria-checked", "false");
    expect(screen.getAllByText("off via provider gemini").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Disabled").length).toBe(2);
    // The provider switch is the one place the whole family turns back on.
    expect(screen.getByRole("switch", { name: "gemini" })).toHaveAttribute("aria-checked", "false");
    expect(screen.getByRole("switch", { name: "gemini" })).not.toBeDisabled();
  });
});

function jsonResponse(value: unknown) {
  return new Response(JSON.stringify(value), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function renderWhoamiPage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <WhoamiPage />
    </QueryClientProvider>,
  );
}

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { WhoamiPage } from "./WhoamiPage";

const WHOAMI_RESULT = {
  adapters: [
    {
      backend: "gemini",
      type: "api",
      authenticated: false,
      modelCount: 0,
      modelError: "set GEMINI_API_KEY or GOOGLE_API_KEY to list models",
    },
    {
      backend: "gemini-cli",
      type: "cli",
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
    expect(screen.getByText("gemini", { selector: "h3" })).toBeInTheDocument();
    expect(screen.getByText("Needs setup")).toBeInTheDocument();
    expect(
      screen.getByText("set GEMINI_API_KEY or GOOGLE_API_KEY to list models"),
    ).toBeInTheDocument();
    expect(screen.getByText("gemini login")).toBeInTheDocument();
    expect(screen.getByText("/home/example/.gemini/oauth_creds.json")).toBeInTheDocument();
    expect(screen.getByText("/usr/local/bin/gemini")).toBeInTheDocument();
    expect(screen.getByText("Gemini 3.5 Flash")).toBeInTheDocument();
    expect(screen.getByText("gemini-3.5-flash")).toBeInTheDocument();
    expect(screen.getByText("2026-05-19")).toBeInTheDocument();
    expect(screen.getByText("low / medium / high")).toBeInTheDocument();
    expect(screen.getByText("Gemini 2.5 Pro")).toBeInTheDocument();
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
});

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

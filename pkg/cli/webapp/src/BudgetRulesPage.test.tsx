import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { BudgetRulesPage } from "./BudgetRulesPage";

const TEAM_RULE = {
  id: "0b6f6c1e-8f0e-4f6f-9c55-2a8d6f0c9a11",
  name: "Team monthly",
  match: { dimensions: { team: "platform-*" }, models: ["anthropic/claude-*"] },
  groupBy: ["team"],
  amount: 100,
  window: "now/M",
  createdAt: "2026-09-01T10:00:00Z",
  updatedAt: "2026-09-01T10:00:00Z",
};

type Call = { method: string; url: string; body?: unknown };
type Route = (call: Call) => Response;

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("BudgetRulesPage", () => {
  it("lists rules with what they match, their amount and window", async () => {
    stubAPI({ "GET /api/v1/budget-rule": () => jsonResponse([TEAM_RULE]) });
    renderPage();

    const row = (await screen.findByText("Team monthly")).closest("tr")!;
    expect(within(row).getByText("team=platform-*")).toBeInTheDocument();
    expect(within(row).getByText("anthropic/claude-*")).toBeInTheDocument();
    expect(within(row).getByText("$100.00")).toBeInTheDocument();
    expect(within(row).getByText("This month")).toBeInTheDocument();
  });

  it("creates a rule from the editor and refreshes the list", async () => {
    let rules: unknown[] = [];
    const calls = stubAPI({
      "GET /api/v1/budget-rule": () => jsonResponse(rules),
      "POST /api/v1/budget-rule": (call) => {
        rules = [{ ...TEAM_RULE, ...(call.body as object) }];
        return jsonResponse(rules[0]);
      },
    });
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: /New rule/ }));
    fireEvent.change(screen.getByPlaceholderText("Platform team monthly"), { target: { value: "Team monthly" } });
    fireEvent.change(screen.getByPlaceholderText("100"), { target: { value: "100" } });
    fireEvent.click(screen.getByRole("button", { name: /Add dimension/ }));
    fireEvent.change(screen.getByLabelText("Dimension 1 key"), { target: { value: "team" } });
    fireEvent.change(screen.getByLabelText("Dimension 1 pattern"), { target: { value: "platform-*" } });
    fireEvent.change(screen.getByPlaceholderText("anthropic/claude-*"), { target: { value: "anthropic/claude-*" } });
    fireEvent.change(screen.getByPlaceholderText("team, user"), { target: { value: "team" } });
    fireEvent.click(screen.getByRole("button", { name: "Create rule" }));

    await waitFor(() => expect(calls.some((call) => call.method === "POST")).toBe(true));
    expect(calls.find((call) => call.method === "POST")?.body).toEqual({
      name: "Team monthly",
      amount: 100,
      window: "now/M",
      groupBy: ["team"],
      match: { dimensions: { team: "platform-*" }, models: ["anthropic/claude-*"] },
    });
    expect(await screen.findByText("team=platform-*")).toBeInTheDocument();
  });

  it("explains an incomplete rule instead of sending it", async () => {
    const calls = stubAPI({ "GET /api/v1/budget-rule": () => jsonResponse([]) });
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: /New rule/ }));
    fireEvent.click(screen.getByRole("button", { name: "Create rule" }));

    expect(await screen.findByText("Name is required.")).toBeInTheDocument();
    expect(screen.getByText("Amount must be a positive number of US dollars.")).toBeInTheDocument();
    expect(calls.some((call) => call.method === "POST")).toBe(false);
  });

  it("deletes a rule after confirmation", async () => {
    let rules: unknown[] = [TEAM_RULE];
    const calls = stubAPI({
      "GET /api/v1/budget-rule": () => jsonResponse(rules),
      [`DELETE /api/v1/budget-rule/${TEAM_RULE.id}`]: () => {
        rules = [];
        return jsonResponse({});
      },
    });
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "Delete Team monthly" }));
    fireEvent.click(screen.getByRole("button", { name: /Delete rule/ }));

    expect(await screen.findByText(/No budget rules yet/)).toBeInTheDocument();
    expect(calls.some((call) => call.method === "DELETE")).toBe(true);
  });
});

function stubAPI(routes: Record<string, Route>) {
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

function renderPage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <BudgetRulesPage />
    </QueryClientProvider>,
  );
}

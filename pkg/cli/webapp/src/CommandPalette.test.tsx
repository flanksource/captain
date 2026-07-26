import { useCallback, useState } from "react";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  CommandPalette,
  useCommandPaletteShortcut,
} from "./CommandPalette";
import { directSessionId } from "./commandPaletteHelpers";
import type { SessionRecord } from "./sessionData";
import type { PromptSummary } from "./promptData";

const fetchSessionSearch = vi.hoisted(() => vi.fn());
const fetchPromptList = vi.hoisted(() => vi.fn());

vi.mock("./sessionData", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./sessionData")>()),
  fetchSessionSearch,
}));

vi.mock("./promptData", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./promptData")>()),
  fetchPromptList,
  resolvePromptOps: () => ({ list: { path: "/api/v1/prompt", method: "GET" } }),
}));

vi.mock("@flanksource/clicky-ui/rpc", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@flanksource/clicky-ui/rpc")>()),
  useOperations: () => ({ operations: [], isLoading: false, error: null }),
}));

// Fixtures use values distinct from anything the component derives, so an
// assertion can only pass if the component actually plumbed them through.
const SESSION_KEY = "055781c7-360a-4eb2-80be-452b3937fcfe";
const SESSION_ID = "019f7c25-9adf-7901-add9-8c46693472fb";
const PROMPT_ID = "local:review-diff";

function session(overrides: Partial<SessionRecord> = {}): SessionRecord {
  return {
    key: SESSION_KEY,
    id: SESSION_ID,
    source: "claude",
    title: "Refactor the billing importer",
    project: "/home/dev/acme/billing",
    toolCalls: 0,
    messages: 0,
    ...overrides,
  } as SessionRecord;
}

function prompt(overrides: Partial<PromptSummary> = {}): PromptSummary {
  return {
    id: PROMPT_ID,
    name: "review-diff",
    sourceKind: "local",
    sourceId: "local",
    source: "local",
    path: "/home/dev/prompts/review-diff.prompt",
    relPath: "prompts/review-diff.prompt",
    writable: true,
    ...overrides,
  } as PromptSummary;
}

function renderPalette(onNavigate = vi.fn(), onClose = vi.fn()) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  render(
    <QueryClientProvider client={client}>
      <CommandPalette open onClose={onClose} onNavigate={onNavigate} />
    </QueryClientProvider>,
  );
  return { onNavigate, onClose };
}

async function type(value: string) {
  const input = screen.getByLabelText("Search sessions and prompts");
  fireEvent.change(input, { target: { value } });
  return input;
}

beforeEach(() => {
  fetchSessionSearch.mockReset();
  fetchPromptList.mockReset();
  fetchSessionSearch.mockResolvedValue({ sessions: [], total: 0 });
  fetchPromptList.mockResolvedValue([]);
});

afterEach(cleanup);

describe("directSessionId", () => {
  it("accepts a token long enough to be an unambiguous id prefix", () => {
    // Identity resolution takes Captain UUIDs and provider-id prefixes, so
    // this must not be gated on a UUID shape.
    expect(directSessionId(SESSION_ID)).toBe(SESSION_ID);
    expect(directSessionId("a1b2c3d4")).toBe("a1b2c3d4");
    expect(directSessionId(`  ${SESSION_KEY}  `)).toBe(SESSION_KEY);
  });

  it("rejects prose and tokens too short to disambiguate", () => {
    expect(directSessionId("billing importer")).toBeNull();
    expect(directSessionId("a1b2c3")).toBeNull();
    expect(directSessionId("")).toBeNull();
  });
});

describe("useCommandPaletteShortcut", () => {
  function Harness() {
    const [count, setCount] = useState(0);
    const toggle = useCallback(() => setCount((c) => c + 1), []);
    useCommandPaletteShortcut(toggle);
    return <span data-testid="count">{count}</span>;
  }

  it("fires on Cmd+K and Ctrl+K but not on a bare k or Alt+Cmd+K", () => {
    render(<Harness />);
    const count = () => screen.getByTestId("count").textContent;

    fireEvent.keyDown(window, { key: "k", metaKey: true });
    expect(count()).toBe("1");

    fireEvent.keyDown(window, { key: "k", ctrlKey: true });
    expect(count()).toBe("2");

    fireEvent.keyDown(window, { key: "k" });
    fireEvent.keyDown(window, { key: "k", metaKey: true, altKey: true });
    expect(count()).toBe("2");
  });
});

describe("CommandPalette", () => {
  it("prompts for input before any query is typed", () => {
    renderPalette();
    expect(
      screen.getByText(/Type to search sessions and prompts/),
    ).toBeInTheDocument();
    expect(fetchSessionSearch).not.toHaveBeenCalled();
  });

  it("searches all projects rather than the active project scope", async () => {
    renderPalette();
    await type("billing");
    await waitFor(() =>
      expect(fetchSessionSearch).toHaveBeenCalledWith({
        query: "billing",
        limit: 20,
      }),
    );
  });

  it("opens the highlighted session on Enter using its Captain UUID key", async () => {
    fetchSessionSearch.mockResolvedValue({ sessions: [session()], total: 1 });
    const { onNavigate, onClose } = renderPalette();
    const input = await type("billing");

    await screen.findByText("Refactor the billing importer");
    fireEvent.keyDown(input, { key: "Enter" });

    expect(onNavigate).toHaveBeenCalledWith(
      `/sessions/${encodeURIComponent(SESSION_KEY)}`,
    );
    expect(onClose).toHaveBeenCalled();
  });

  it("opens a prompt result at its prompt route", async () => {
    fetchPromptList.mockResolvedValue([prompt()]);
    const { onNavigate } = renderPalette();
    const input = await type("review");

    await screen.findByText("review-diff");
    fireEvent.keyDown(input, { key: "Enter" });

    expect(onNavigate).toHaveBeenCalledWith(
      `/prompts/${encodeURIComponent(PROMPT_ID)}`,
    );
  });

  it("wraps arrow navigation across the session and prompt groups", async () => {
    fetchSessionSearch.mockResolvedValue({ sessions: [session()], total: 1 });
    fetchPromptList.mockResolvedValue([prompt()]);
    const { onNavigate } = renderPalette();
    const input = await type("review");

    await screen.findByText("review-diff");

    // Two rows total, starting on the session. ArrowDown crosses into the
    // prompt group...
    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onNavigate).toHaveBeenCalledWith(
      `/prompts/${encodeURIComponent(PROMPT_ID)}`,
    );

    // ...and a further ArrowDown wraps past the end back to the session.
    onNavigate.mockClear();
    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onNavigate).toHaveBeenCalledWith(
      `/sessions/${encodeURIComponent(SESSION_KEY)}`,
    );

    // ArrowUp wraps in the opposite direction, off the first row to the last.
    onNavigate.mockClear();
    fireEvent.keyDown(input, { key: "ArrowUp" });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onNavigate).toHaveBeenCalledWith(
      `/prompts/${encodeURIComponent(PROMPT_ID)}`,
    );
  });

  it("offers a direct-open row that defers resolution to the session route", async () => {
    const { onNavigate } = renderPalette();
    const input = await type(SESSION_ID);

    await screen.findByText("Open session by id");
    fireEvent.keyDown(input, { key: "Enter" });

    expect(onNavigate).toHaveBeenCalledWith(
      `/sessions/${encodeURIComponent(SESSION_ID)}`,
    );
  });

  it("does not offer a direct-open row for a multi-word query", async () => {
    renderPalette();
    await type("billing importer");
    await waitFor(() => expect(fetchSessionSearch).toHaveBeenCalled());
    expect(screen.queryByText("Open session by id")).not.toBeInTheDocument();
  });

  it("caps each group and reports the remainder", async () => {
    const many = Array.from({ length: 11 }, (_, i) =>
      session({ key: `key-${i}`, id: `id-${i}`, title: `Session ${i}` }),
    );
    fetchSessionSearch.mockResolvedValue({ sessions: many, total: many.length });
    renderPalette();
    await type("session");

    await screen.findByText("Session 0");
    expect(screen.getByText("Session 7")).toBeInTheDocument();
    expect(screen.queryByText("Session 8")).not.toBeInTheDocument();
    expect(screen.getByText("+3 more")).toBeInTheDocument();
  });

  it("reports when nothing matches", async () => {
    renderPalette();
    // Multi-word so no direct-open row is offered; otherwise the palette always
    // has at least one row and the empty state is unreachable.
    await type("no such thing");
    expect(
      await screen.findByText(/No sessions or prompts match/),
    ).toBeInTheDocument();
  });
});

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { ContextCell } from "./SessionTable";
import type { SessionRecord } from "./sessionData";

const SESSION: SessionRecord = {
  key: "captain-session-context-meter",
  id: "01a09ed8-3167-72d1-8d12-c2935a8752b6",
  source: "codex",
  provider: "openai",
  modelMode: "cli",
  model: "gpt-5.6-luna",
  reasoningEffort: "high",
  messages: 38,
  toolCalls: 14,
  context: {
    usedTokens: 88_818,
    windowTokens: 258_400,
    freePercent: 66,
  },
  tokens: {
    inputTokens: 86_514,
    outputTokens: 2_223,
    cacheReadTokens: 855_296,
    totalTokens: 947_004,
  },
  costUsd: 0.04064152,
};

afterEach(cleanup);

describe("ContextCell", () => {
  it("renders context-window occupancy through ContextMeter", async () => {
    render(<ContextCell session={SESSION} />);

    const meter = screen.getByLabelText("Context 34% used");
    fireEvent.mouseEnter(meter);

    expect(await screen.findByText("Window")).toBeInTheDocument();
    expect(screen.getByText("89k / 258k")).toBeInTheDocument();
    expect(screen.getByText("66%")).toBeInTheDocument();
    expect(screen.getByText("947k")).toBeInTheDocument();
  });

  it("renders a placeholder when context is unavailable", () => {
    render(<ContextCell session={{ ...SESSION, context: undefined }} />);

    expect(screen.getByText("--")).toBeInTheDocument();
    expect(screen.queryByLabelText(/Context .* used/)).not.toBeInTheDocument();
  });
});

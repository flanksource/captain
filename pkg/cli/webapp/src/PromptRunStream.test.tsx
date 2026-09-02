import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { PromptRunStream } from "./PromptRunStream";

const useSessionChatMock = vi.hoisted(() => vi.fn());

vi.mock("./hooks/useSessionChat", () => ({
  useSessionChat: useSessionChatMock,
}));

describe("PromptRunStream", () => {
  beforeEach(() => {
    useSessionChatMock.mockReturnValue({
      messages: [],
      summary: {
        runId: "run-4a82",
        sessionId: "session-91bd",
        model: "claude-sonnet-5",
        provider: "anthropic",
        mode: "api",
        success: false,
        error: "provider rejected the request",
      },
      status: "error",
      error: "provider rejected the request",
      run: {
        runId: "run-4a82",
        sessionId: "session-91bd",
        status: "error",
        chat: false,
        model: "claude-sonnet-5",
        provider: "anthropic",
        mode: "api",
      },
    });
  });

  it("shows failed-run diagnostics including the session UID", () => {
    render(<PromptRunStream runID="run-4a82" />);

    expect(screen.getByRole("alert")).toHaveTextContent(
      "provider rejected the request",
    );
    expect(screen.getByText("Run UID")).toBeInTheDocument();
    expect(screen.getByText("run-4a82")).toBeInTheDocument();
    expect(screen.getByText("Session UID")).toBeInTheDocument();
    expect(screen.getByText("session-91bd")).toBeInTheDocument();
    expect(screen.getByText("claude-sonnet-5")).toBeInTheDocument();
    expect(screen.getByText("anthropic")).toBeInTheDocument();
    expect(screen.getByText("api")).toBeInTheDocument();
    expect(
      screen.getByText("Run failed before session activity."),
    ).toBeInTheDocument();
    expect(screen.queryByText("Starting run…")).not.toBeInTheDocument();
  });
});

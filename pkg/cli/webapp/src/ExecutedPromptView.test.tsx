import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ExecutedPromptView } from "./ExecutedPromptView";

describe("ExecutedPromptView", () => {
  it("shows the latest execution and links to its saved session", () => {
    render(
      <ExecutedPromptView
        name="plan"
        prompt={{
          name: "plan",
          origin: "gavel.todos",
          runId: "run-1",
          sessionId: "session-1",
          queuedAt: "2026-10-18T09:00:00Z",
        }}
        loading={false}
        error={undefined}
      />,
    );

    expect(screen.getByText("gavel.todos")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "View latest session" })).toHaveAttribute(
      "href",
      "/sessions/session-1",
    );
    expect(screen.queryByRole("button", { name: /run|save|delete/i })).toBeNull();
  });
});

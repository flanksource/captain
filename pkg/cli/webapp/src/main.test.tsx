import { screen } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@flanksource/clicky-ui/hooks", () => ({
  DensityProvider: ({ children }: PropsWithChildren) => children,
  ThemeProvider: ({ children }: PropsWithChildren) => children,
}));

vi.mock("@flanksource/clicky-ui/monaco", () => ({
  MonacoProvider: ({ children }: PropsWithChildren) => children,
}));

vi.mock("./App", () => ({
  App: () => {
    throw new Error("session renderer failed");
  },
}));

describe("application error boundary", () => {
  beforeEach(() => {
    document.body.innerHTML = '<div id="root"></div>';
    vi.spyOn(console, "error").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("renders diagnostics when a route throws", async () => {
    await import("./main");

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "session renderer failed",
    );
  });
});

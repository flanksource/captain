import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { TimingBadge } from "./TimingBadge";

describe("TimingBadge", () => {
  it("renders the total and detailed phase labels", () => {
    render(
      <TimingBadge
        metrics={[
          { name: "total", dur: 12035.3 },
          { name: "command", dur: 35.4 },
          { name: "format", dur: 11990.2 },
          { name: "database", dur: 0.2 },
          { name: "lookup", dur: 8.1 },
          { name: "hydrate", dur: 26.7 },
          { name: "parse", dur: 24.6 },
          { name: "prompt_runs", dur: 1.1 },
        ]}
      />,
    );

    expect(screen.getByTitle("Server timing — hover for the breakdown")).toHaveTextContent(
      "12.0 s",
    );
    for (const label of [
      "Command",
      "Format response",
      "Database",
      "Session lookup",
      "Hydrate sessions",
      "Parse history",
      "Prompt runs",
    ]) {
      expect(screen.getByText(label)).toBeInTheDocument();
    }
  });

  it("renders nothing without timing metrics", () => {
    const { container } = render(<TimingBadge metrics={undefined} />);
    expect(container).toBeEmptyDOMElement();
  });
});

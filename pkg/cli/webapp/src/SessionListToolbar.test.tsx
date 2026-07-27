import type { ReactNode } from "react";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

type FilterBarProps = {
  search: { ariaLabel: string };
  leading: ReactNode;
  dateRange: { emptyLabel: string };
  trailing: ReactNode;
};

type SegmentedControlProps = {
  value: string;
  options: Array<{ id: string; label: string }>;
  onChange: (value: string) => void;
  "aria-label": string;
};

vi.mock("@flanksource/clicky-ui/components", () => ({
  FilterBar: ({ search, leading, dateRange, trailing }: FilterBarProps) => (
    <div>
      <input aria-label={search.ariaLabel} />
      {leading}
      <button type="button" aria-label="Date range filter">
        {dateRange.emptyLabel}
      </button>
      {trailing}
    </div>
  ),
  SegmentedControl: ({
    value,
    options,
    onChange,
    "aria-label": ariaLabel,
  }: SegmentedControlProps) => (
    <div role="radiogroup" aria-label={ariaLabel}>
      {options.map((option) => (
        <button
          key={option.id}
          type="button"
          role="radio"
          aria-checked={option.id === value}
          onClick={() => onChange(option.id)}
        >
          {option.label}
        </button>
      ))}
    </div>
  ),
}));

import { SessionListToolbar } from "./SessionListToolbar";
import type { SessionListFilters } from "./sessionListFilters";

const FILTERS: SessionListFilters = {
  mode: "live",
  source: "all",
  query: "",
  from: "",
  to: "",
};

afterEach(cleanup);

describe("SessionListToolbar", () => {
  it("renders the live/all and date controls and emits an all-session filter", () => {
    const onFiltersChange = vi.fn();
    render(
      <SessionListToolbar
        filters={FILTERS}
        onFiltersChange={onFiltersChange}
        shown={4}
        total={9}
        loading={false}
      />,
    );

    const mode = screen.getByRole("radiogroup", { name: "Session mode" });
    expect(within(mode).getByRole("radio", { name: "Live" })).toBeChecked();
    expect(within(mode).getByRole("radio", { name: "All" })).not.toBeChecked();
    expect(
      screen.getByRole("button", { name: "Date range filter" }),
    ).toBeInTheDocument();

    fireEvent.click(within(mode).getByRole("radio", { name: "All" }));
    expect(onFiltersChange).toHaveBeenCalledWith({
      ...FILTERS,
      mode: "all",
    });
  });

  it("hides live-only summary cards in all-session mode", () => {
    render(
      <SessionListToolbar
        filters={{ ...FILTERS, mode: "all" }}
        onFiltersChange={vi.fn()}
        summary={{
          totalSessions: 9,
          liveSessions: 4,
          activeSessions: 2,
          stoppedSessions: 2,
          alertSessions: 1,
          totalTokens: 1200,
          costUsd: 0.3,
          lowestContextFree: 40,
        }}
        shown={9}
        total={9}
        loading={false}
      />,
    );

    expect(screen.queryByText("Active")).not.toBeInTheDocument();
    expect(screen.getByText("9 shown / 9 total")).toBeInTheDocument();
  });
});

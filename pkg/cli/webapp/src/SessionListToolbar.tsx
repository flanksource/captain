import type { ReactNode } from "react";
import {
  FilterBar,
  SegmentedControl,
} from "@flanksource/clicky-ui/components";
import { TimingBadge } from "./TimingBadge";
import {
  SOURCE_OPTIONS,
  formatCompactNumber,
  formatCost,
  type SessionDashboard,
} from "./sessionData";
import {
  type SessionListFilters,
  type SessionMode,
} from "./sessionListFilters";
import type { TimingMetric } from "./serverTiming";

const MODE_OPTIONS = [
  { id: "live", label: "Live" },
  { id: "all", label: "All" },
] satisfies Array<{ id: SessionMode; label: string }>;

export function SessionListToolbar({
  filters,
  onFiltersChange,
  summary,
  timing,
  shown,
  total,
  loading,
}: {
  filters: SessionListFilters;
  onFiltersChange: (filters: SessionListFilters) => void;
  summary?: SessionDashboard;
  timing?: TimingMetric[];
  shown: number;
  total: number;
  loading: boolean;
}) {
  return (
    <div className="shrink-0 space-y-density-2 border-b border-border p-density-3">
      <FilterBar
        search={{
          value: filters.query,
          onChange: (query) => onFiltersChange({ ...filters, query }),
          placeholder: "Search sessions",
          ariaLabel: "Search sessions",
        }}
        leading={
          <FilterControl label="Sessions">
            <SegmentedControl
              value={filters.mode}
              options={MODE_OPTIONS}
              onChange={(mode) => onFiltersChange({ ...filters, mode })}
              size="sm"
              aria-label="Session mode"
            />
          </FilterControl>
        }
        dateRange={{
          from: filters.from,
          to: filters.to,
          onApply: (from, to) =>
            onFiltersChange({ ...filters, from, to }),
          emptyLabel: "Any date",
        }}
        trailing={
          <FilterControl label="Source">
            <SegmentedControl
              value={filters.source}
              options={SOURCE_OPTIONS}
              onChange={(source) => onFiltersChange({ ...filters, source })}
              size="sm"
              aria-label="Session source"
            />
          </FilterControl>
        }
        overflowMode="wrap"
      />
      {filters.mode === "live" ? (
        <SessionSummary summary={summary} loading={loading} />
      ) : null}
      <div className="flex items-center justify-between gap-density-2 text-xs text-muted-foreground">
        <span>{loading ? "Loading..." : `${shown} shown / ${total} total`}</span>
        <TimingBadge metrics={timing} align="right" />
      </div>
    </div>
  );
}

function FilterControl({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <div className="flex items-center gap-1.5">
      <span className="text-[10px] font-medium uppercase text-muted-foreground">
        {label}
      </span>
      {children}
    </div>
  );
}

function SessionSummary({
  summary,
  loading,
}: {
  summary?: SessionDashboard;
  loading: boolean;
}) {
  if (!summary && loading) {
    return null;
  }
  const values = [
    [
      "Live",
      summary ? `${summary.liveSessions}/${summary.totalSessions}` : "--",
    ],
    ["Active", summary?.activeSessions ?? 0],
    ["Alerts", summary?.alertSessions ?? 0],
    ["Tokens", formatCompactNumber(summary?.totalTokens ?? 0)],
    ["Cost", formatCost(summary?.costUsd ?? 0)],
    [
      "Context",
      summary?.lowestContextFree !== undefined
        ? `${summary.lowestContextFree}%`
        : "--",
    ],
  ];
  return (
    <div className="grid grid-cols-3 gap-1.5 md:grid-cols-6">
      {values.map(([label, value]) => (
        <div
          key={label}
          className="min-w-0 rounded border border-border px-2 py-1"
        >
          <div className="truncate text-[10px] uppercase text-muted-foreground">
            {label}
          </div>
          <div className="truncate text-xs font-medium">{value}</div>
        </div>
      ))}
    </div>
  );
}

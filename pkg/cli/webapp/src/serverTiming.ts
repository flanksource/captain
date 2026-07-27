// Parsing and formatting for the W3C Server-Timing response header the captain
// session endpoints set. The browser reads the request and session phase wall
// times back to show a timing badge with a detailed breakdown.

export interface TimingMetric {
  /** Server-Timing metric token, e.g. "find", "total". */
  name: string;
  /** Human-readable label from the metric's `desc`, when present. */
  desc?: string;
  /** Duration in milliseconds. */
  dur: number;
}

/** Friendly labels for the phases captain's session endpoints emit. Unknown
 * phase names fall back to the raw token. */
const PHASE_LABELS: Record<string, string> = {
  total: "Total",
  command: "Command",
  format: "Format response",
  database: "Database",
  lookup: "Session lookup",
  hydrate: "Hydrate sessions",
  find: "Find files",
  parse: "Parse history",
  prompt_runs: "Prompt runs",
  enrich: "Live processes",
};

export function phaseLabel(metric: TimingMetric): string {
  return metric.desc || PHASE_LABELS[metric.name] || metric.name;
}

/** Parses a Server-Timing header into its metrics. Each entry is
 * `name;desc="Label";dur=12.3`; a missing `dur` defaults to 0 and a
 * null/empty header yields an empty list. */
export function parseServerTiming(header: string | null | undefined): TimingMetric[] {
  if (!header) return [];
  const metrics: TimingMetric[] = [];
  for (const entry of header.split(",")) {
    const tokens = entry.split(";").flatMap((token) => {
      const trimmed = token.trim();
      return trimmed ? [trimmed] : [];
    });
    if (tokens.length === 0) continue;
    const metric: TimingMetric = { name: tokens[0], dur: 0 };
    for (const token of tokens.slice(1)) {
      const eq = token.indexOf("=");
      if (eq < 0) continue;
      const key = token.slice(0, eq).trim();
      let value = token.slice(eq + 1).trim();
      if (value.length >= 2 && value.startsWith('"') && value.endsWith('"')) {
        value = value.slice(1, -1);
      }
      if (key === "desc") metric.desc = value;
      else if (key === "dur") metric.dur = Number(value) || 0;
    }
    metrics.push(metric);
  }
  return metrics;
}

/** Formats a millisecond duration for display: sub-10 ms keeps one decimal,
 * up to a second rounds to whole ms, larger switches to seconds. */
export function formatTimingMs(ms: number): string {
  if (ms >= 1000) return `${(ms / 1000).toFixed(1)} s`;
  if (ms < 10) return `${ms.toFixed(1)} ms`;
  return `${Math.round(ms)} ms`;
}

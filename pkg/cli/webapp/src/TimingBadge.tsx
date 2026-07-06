import { formatTimingMs, phaseLabel, type TimingMetric } from "./serverTiming";

/** A server-timing badge with a hover dropdown that breaks the request's total
 * wall time down by phase. The `total` metric is the headline; the remaining
 * phases are listed with a bar proportional to the slowest. Pure CSS
 * group-hover — no popover library — matching the app's badge styling. */
export function TimingBadge({
  metrics,
  align = "left",
}: {
  metrics: TimingMetric[] | undefined;
  align?: "left" | "right";
}) {
  if (!metrics || metrics.length === 0) return null;
  const total = metrics.find((metric) => metric.name === "total");
  const phases = metrics.filter((metric) => metric.name !== "total" && metric.dur > 0);
  if (!total && phases.length === 0) return null;
  const headline = total?.dur ?? phases.reduce((sum, phase) => sum + phase.dur, 0);
  const slowest = Math.max(...phases.map((phase) => phase.dur), 1);
  return (
    <div className="group relative inline-flex">
      <span
        className="inline-flex cursor-default items-center gap-1 rounded border border-border px-1.5 py-0.5 text-[10px] tabular-nums text-muted-foreground"
        title="Server timing — hover for the breakdown"
      >
        <ClockIcon />
        {formatTimingMs(headline)}
      </span>
      <div
        className={[
          "invisible absolute top-full z-50 mt-1 w-56 rounded-md border border-border bg-popover p-2.5 text-popover-foreground opacity-0 shadow-lg transition-opacity group-hover:visible group-hover:opacity-100",
          align === "right" ? "right-0" : "left-0",
        ].join(" ")}
      >
        <div className="mb-2 flex items-center justify-between text-xs font-medium">
          <span>Server timing</span>
          <span className="tabular-nums text-muted-foreground">{formatTimingMs(headline)}</span>
        </div>
        {phases.length === 0 ? (
          <div className="text-[11px] text-muted-foreground">No phase breakdown.</div>
        ) : (
          <ul className="space-y-1.5">
            {phases.map((phase) => (
              <li key={phase.name}>
                <div className="flex items-center justify-between text-[11px]">
                  <span className="text-muted-foreground">{phaseLabel(phase)}</span>
                  <span className="tabular-nums">{formatTimingMs(phase.dur)}</span>
                </div>
                <div className="mt-0.5 h-1 overflow-hidden rounded-full bg-muted">
                  <div
                    className="h-full rounded-full bg-primary/70"
                    style={{ width: `${Math.max((phase.dur / slowest) * 100, 4)}%` }}
                  />
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function ClockIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-3"
      aria-hidden="true"
    >
      <circle cx="12" cy="12" r="9" />
      <path d="M12 7v5l3 2" />
    </svg>
  );
}

import { useState } from "react";
import { useTaskRuns } from "@flanksource/clicky-ui/hooks";
import { TaskManager } from "@flanksource/clicky-ui/data";

const TASK_BASE = "/api/captain";

/**
 * RunningPrompts surfaces in-flight prompt runs, tracked as clicky task groups
 * of kind "prompt". Badge is a compact header indicator; RunsTab is the full
 * cross-run list backed by clicky's TaskManager. Both share the same task API.
 */
function Badge({ onSelectRun }: { onSelectRun: (id: string) => void }) {
  const { runs } = useTaskRuns({ kind: "prompt", status: "running", basePath: TASK_BASE });
  const [open, setOpen] = useState(false);

  if (runs.length === 0) return null;

  return (
    <div className="relative">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex items-center gap-density-1 rounded-full border border-border px-density-2 py-density-1 text-xs text-muted-foreground hover:bg-muted"
      >
        <span className="size-2 rounded-full bg-blue-500 animate-pulse" />
        {runs.length} running
      </button>
      {open && (
        <div className="absolute right-0 z-20 mt-density-1 w-72 rounded-md border border-border bg-background p-density-1 shadow-md">
          {runs.map((run) => (
            <button
              key={run.id}
              type="button"
              onClick={() => {
                onSelectRun(run.id);
                setOpen(false);
              }}
              className="flex w-full items-center justify-between gap-density-2 rounded px-density-2 py-density-1 text-left text-xs hover:bg-muted"
            >
              <span className="truncate">{run.name}</span>
              <span className="shrink-0 text-muted-foreground">{run.status}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function RunsTab({
  activeRunID,
  onSelectRun,
}: {
  activeRunID?: string;
  onSelectRun: (id: string | undefined) => void;
}) {
  return (
    <div className="h-full overflow-auto">
      <TaskManager
        basePath={TASK_BASE}
        kind="prompt"
        selectedId={activeRunID}
        onSelectRun={(id) => onSelectRun(id ?? undefined)}
      />
    </div>
  );
}

export const RunningPrompts = { Badge, RunsTab };

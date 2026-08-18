import { useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Button } from "@flanksource/clicky-ui/components";
import { Badge } from "@flanksource/clicky-ui/data";

import {
  fetchGitAgentTask,
  fetchGitAgentTasks,
  isTaskOpen,
  type GitAgentTask,
  type GitAgentTaskStatus,
} from "./sandboxData";

const STATUS_FILTERS: Array<{ id: string; label: string }> = [
  { id: "", label: "All" },
  { id: "running", label: "Running" },
  { id: "accepted", label: "Accepted" },
  { id: "rejected", label: "Rejected" },
  { id: "errored", label: "Errored" },
];

const STATUS_TONE: Record<GitAgentTaskStatus, string> = {
  dispatched: "text-muted-foreground",
  running: "text-sky-600 dark:text-sky-400",
  accepted: "text-emerald-600 dark:text-emerald-400",
  rejected: "text-amber-600 dark:text-amber-400",
  errored: "text-destructive",
  timed_out: "text-destructive",
};

export function GitAgentTasks() {
  const [status, setStatus] = useState("");
  const [selected, setSelected] = useState<GitAgentTask | undefined>();

  const tasks = useQuery({
    queryKey: ["git-agent-tasks", status],
    queryFn: () => fetchGitAgentTasks(status ? { status } : {}),
    // History is written by the ingest watcher on its backfill pass, so this
    // does not need to be live; a modest refetch keeps a long-lived tab current.
    refetchInterval: 30_000,
  });

  if (tasks.error) {
    return (
      <p role="alert" className="text-xs text-destructive">
        {tasks.error instanceof Error ? tasks.error.message : String(tasks.error)}
      </p>
    );
  }

  return (
    <div className="space-y-density-3">
      <div className="flex flex-wrap items-center gap-density-2">
        {STATUS_FILTERS.map((filter) => (
          <Button
            key={filter.id || "all"}
            size="sm"
            variant={status === filter.id ? "secondary" : "ghost"}
            onClick={() => setStatus(filter.id)}
          >
            {filter.label}
          </Button>
        ))}
      </div>

      {tasks.isLoading ? (
        <p className="text-xs text-muted-foreground">Loading tasks…</p>
      ) : (tasks.data?.length ?? 0) === 0 ? (
        <p className="text-xs text-muted-foreground">
          No remote tasks recorded yet. Dispatching a prompt with a git-agent
          sandbox records its history here.
        </p>
      ) : (
        <table className="w-full text-left text-xs">
          <thead className="text-muted-foreground">
            <tr>
              <Th>Task</Th>
              <Th>Status</Th>
              <Th>Agent</Th>
              <Th>Attempts</Th>
              <Th>Repository</Th>
              <Th>Dispatched</Th>
            </tr>
          </thead>
          <tbody>
            {tasks.data?.map((task) => (
              <tr
                key={task.id}
                className="cursor-pointer border-t border-border hover:bg-muted/50"
                onClick={() => setSelected(task)}
              >
                <Td>
                  <span className="font-mono">{task.taskId}</span>
                </Td>
                <Td>
                  <span className={STATUS_TONE[task.status]}>{task.status}</span>
                </Td>
                <Td>{task.agent || "—"}</Td>
                <Td>
                  {task.attempts}
                  {task.maxAttempts ? ` / ${task.maxAttempts}` : ""}
                </Td>
                <Td>
                  <span className="font-mono">{task.repository || "—"}</span>
                </Td>
                <Td>{new Date(task.dispatchedAt).toLocaleString()}</Td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {selected && (
        <GitAgentTaskDetailPanel
          task={selected}
          onClose={() => setSelected(undefined)}
        />
      )}
    </div>
  );
}

function GitAgentTaskDetailPanel({
  task,
  onClose,
}: {
  task: GitAgentTask;
  onClose: () => void;
}) {
  const detail = useQuery({
    queryKey: ["git-agent-task", task.mailbox, task.taskId],
    queryFn: () => fetchGitAgentTask(task.taskId, task.mailbox),
    // An open task is still accruing verdicts; a concluded one never changes.
    refetchInterval: isTaskOpen(task) ? 5_000 : false,
  });

  return (
    <div className="rounded-md border border-border bg-muted/30 p-density-3">
      <div className="mb-density-2 flex items-center justify-between">
        <h3 className="font-mono text-xs font-medium">{task.taskId}</h3>
        <Button size="sm" variant="ghost" onClick={onClose}>
          Close
        </Button>
      </div>

      <dl className="mb-density-3 grid grid-cols-[auto_1fr] gap-x-density-3 gap-y-1 text-xs">
        <Dt>Status</Dt>
        <dd className={STATUS_TONE[task.status]}>{task.status}</dd>
        <Dt>Base</Dt>
        <dd className="font-mono">{task.base}</dd>
        <Dt>Dispatch commit</Dt>
        <dd className="font-mono">{task.dispatchCommit}</dd>
        {task.integratedBranch && (
          <>
            <Dt>Integrated onto</Dt>
            <dd className="font-mono">{task.integratedBranch}</dd>
          </>
        )}
        {task.relay && (
          <>
            <Dt>Relay</Dt>
            <dd>{task.relay}</dd>
          </>
        )}
      </dl>

      {detail.error ? (
        <p role="alert" className="text-xs text-destructive">
          {detail.error instanceof Error
            ? detail.error.message
            : String(detail.error)}
        </p>
      ) : (detail.data?.attempts.length ?? 0) === 0 ? (
        <p className="text-xs text-muted-foreground">
          No verdict yet — the agent has not submitted this attempt.
        </p>
      ) : (
        <ol className="space-y-density-2">
          {detail.data?.attempts.map((attempt) => (
            <li
              key={`${attempt.attempt}-${attempt.tier}`}
              className="rounded border border-border bg-background p-density-2"
            >
              <div className="flex items-center gap-density-2">
                <Badge>attempt {attempt.attempt}</Badge>
                <span className="text-muted-foreground">{attempt.tier}</span>
                <span
                  className={
                    attempt.status === "accepted"
                      ? "text-emerald-600 dark:text-emerald-400"
                      : "text-amber-600 dark:text-amber-400"
                  }
                >
                  {attempt.status}
                </span>
              </div>
              {(attempt.findings?.length ?? 0) > 0 && (
                <ul className="mt-1 space-y-0.5">
                  {attempt.findings?.map((finding, index) => (
                    <li key={index} className="text-muted-foreground">
                      <span className="font-mono">{String(finding.hook ?? "")}</span>
                      {finding.message ? `: ${String(finding.message)}` : ""}
                    </li>
                  ))}
                </ul>
              )}
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}

function Th({ children }: { children: ReactNode }) {
  return <th className="px-density-2 py-1 font-medium">{children}</th>;
}

function Td({ children }: { children: ReactNode }) {
  return <td className="px-density-2 py-1.5">{children}</td>;
}

function Dt({ children }: { children: ReactNode }) {
  return <dt className="text-muted-foreground">{children}</dt>;
}

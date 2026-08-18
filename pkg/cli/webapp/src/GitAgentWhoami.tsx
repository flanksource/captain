import { useQuery } from "@tanstack/react-query";
import { Button } from "@flanksource/clicky-ui/components";
import { Badge } from "@flanksource/clicky-ui/data";

import { fetchGitAgentWhoami, type AgentWhoamiAdapter } from "./sandboxData";

export function GitAgentWhoami({
  backend,
  agent,
}: {
  backend: string;
  agent: string;
}) {
  const query = useQuery({
    queryKey: ["git-agent-whoami", backend, agent],
    queryFn: () => fetchGitAgentWhoami({ backend, name: agent }),
  });

  if (query.isLoading) {
    return (
      <p className="text-xs text-muted-foreground">
        Inspecting agent runtimes…
      </p>
    );
  }
  if (query.error) {
    return (
      <div className="flex items-center justify-between gap-density-2">
        <p role="alert" className="text-xs text-destructive">
          {query.error instanceof Error
            ? query.error.message
            : String(query.error)}
        </p>
        <Button
          size="sm"
          variant="outline"
          onClick={() => void query.refetch()}
        >
          Retry
        </Button>
      </div>
    );
  }

  const adapters = query.data?.adapters ?? [];
  const ready = adapters.filter(adapterReady).length;
  return (
    <section
      aria-label={`${agent} runtime identity`}
      className="space-y-density-2"
    >
      <div className="flex flex-wrap items-center justify-between gap-density-2">
        <div className="flex flex-wrap gap-density-1">
          <Badge variant="outline">
            {ready} ready adapter{ready === 1 ? "" : "s"}
          </Badge>
          <Badge variant="outline">
            {adapters.reduce((total, adapter) => total + adapter.modelCount, 0)}{" "}
            models
          </Badge>
        </div>
        <Button
          size="sm"
          variant="outline"
          disabled={query.isFetching}
          onClick={() => void query.refetch()}
        >
          {query.isFetching ? "Refreshing…" : "Refresh"}
        </Button>
      </div>
      {adapters.length === 0 ? (
        <p className="text-xs text-muted-foreground">No runtimes reported.</p>
      ) : (
        <div className="grid gap-density-2 lg:grid-cols-2">
          {adapters.map((adapter) => (
            <AdapterIdentity key={adapter.backend} adapter={adapter} />
          ))}
        </div>
      )}
    </section>
  );
}

function AdapterIdentity({ adapter }: { adapter: AgentWhoamiAdapter }) {
  const ready = adapterReady(adapter);
  return (
    <article className="space-y-density-2 rounded-md border border-border bg-background p-density-2">
      <div className="flex flex-wrap items-center gap-density-2">
        <span className="font-mono text-xs font-semibold">
          {adapter.backend}
        </span>
        <Badge size="xs" tone={ready ? "success" : "warning"}>
          {adapter.disabled ? "Disabled" : ready ? "Ready" : "Needs setup"}
        </Badge>
        <span className="text-xs text-muted-foreground">
          {adapter.provider} / {adapter.mode}
        </span>
      </div>
      {(adapter.authMethod ||
        adapter.authDetail ||
        adapter.binary ||
        adapter.binaryMissing ||
        adapter.dependencyMissing ||
        adapter.runtimeError) && (
        <dl className="grid grid-cols-[auto_1fr] gap-x-density-2 text-xs">
          {adapter.authMethod && (
            <Detail label="Auth" value={adapter.authMethod} />
          )}
          {adapter.authDetail && (
            <Detail label="Identity" value={adapter.authDetail} />
          )}
          {adapter.binary && <Detail label="Binary" value={adapter.binary} />}
          {adapter.binaryMissing && (
            <Detail label="Missing" value={adapter.binaryMissing} />
          )}
          {adapter.dependencyMissing && (
            <Detail label="Dependency" value={adapter.dependencyMissing} />
          )}
          {adapter.runtimeError && (
            <Detail label="Runtime" value={adapter.runtimeError} />
          )}
        </dl>
      )}
      {adapter.modelError && (
        <p className="text-xs text-amber-600 dark:text-amber-400">
          {adapter.modelError}
        </p>
      )}
      {(adapter.models?.length ?? 0) > 0 ? (
        <ul
          className="flex flex-wrap gap-1"
          aria-label={`${adapter.backend} models`}
        >
          {adapter.models?.map((model) => (
            <li
              key={model}
              className="rounded border border-border px-1.5 py-0.5 font-mono text-[10px]"
            >
              {model}
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-xs text-muted-foreground">No models reported.</p>
      )}
    </article>
  );
}

function Detail({ label, value }: { label: string; value: string }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-all font-mono">{value}</dd>
    </>
  );
}

function adapterReady(adapter: AgentWhoamiAdapter) {
  if (!adapter.authenticated || adapter.disabled) return false;
  if (adapter.type !== "cli") return true;
  return (
    Boolean(adapter.binary || adapter.provisioner) &&
    !adapter.binaryMissing &&
    !adapter.dependencyMissing &&
    !adapter.runtimeError
  );
}

import { Badge } from "@flanksource/clicky-ui/data";

import type { DeployResult } from "./sandboxData";

/**
 * Every mutation the deploy intends, from the same builder the CLI's --dry-run
 * prints — so the preview cannot describe a different deployment than the one
 * that runs.
 */
export function PreviewPlan({ result }: { result: DeployResult }) {
  return (
    <div className="grid gap-density-2 rounded-md border border-border bg-muted/40 p-density-3">
      <p className="text-xs font-medium">This would:</p>
      <ol className="grid gap-1 text-xs">
        {(result.mutations ?? []).map((mutation, index) => (
          <li key={`${index}-${mutation}`} className="font-mono break-all">
            {mutation}
          </li>
        ))}
      </ol>
      <dl className="grid grid-cols-[auto_1fr] gap-x-density-3 gap-y-1 border-t border-border pt-density-2 text-xs">
        <dt className="text-muted-foreground">Dispatched to at</dt>
        <dd className="font-mono break-all">
          {result.advertise}{" "}
          <span className="text-muted-foreground">({result.advertiseFrom})</span>
        </dd>
        <RouteRow result={result} />
        <dt className="text-muted-foreground">Security</dt>
        <dd>{result.security}</dd>
        <dt className="text-muted-foreground">Credentials</dt>
        <dd
          className={
            result.credentials.startsWith("none")
              ? "text-amber-600 dark:text-amber-400"
              : undefined
          }
        >
          {result.credentials}
        </dd>
      </dl>
    </div>
  );
}

export function DeployedSummary({ result }: { result: DeployResult }) {
  return (
    <div className="grid gap-density-3">
      <p className="text-xs text-muted-foreground">
        {result.enrolled
          ? `${result.agent} is enrolled and dispatchable.`
          : `${result.agent} was created; it enrolls when the sidecar finishes starting.`}
      </p>
      <dl className="grid grid-cols-[auto_1fr] gap-x-density-3 gap-y-1 text-xs">
        <dt className="text-muted-foreground">Workload</dt>
        <dd className="font-mono break-all">
          {result.workload}
          {result.namespace ? ` (${result.namespace})` : ""}
        </dd>
        <dt className="text-muted-foreground">Dispatched to at</dt>
        <dd className="font-mono break-all">{result.advertise}</dd>
        <RouteRow result={result} />
        <dt className="text-muted-foreground">State volume</dt>
        <dd className="font-mono break-all">{result.volume}</dd>
        {(result.objects?.length ?? 0) > 0 && (
          <>
            <dt className="text-muted-foreground">Created</dt>
            <dd>
              <ul className="grid gap-0.5">
                {result.objects?.map((object) => (
                  <li key={object} className="font-mono break-all">
                    {object}
                  </li>
                ))}
              </ul>
            </dd>
          </>
        )}
      </dl>
      {result.credentials.startsWith("none") && (
        <p role="alert" className="text-xs text-amber-600 dark:text-amber-400">
          {result.credentials}. It will enrol and go ready, then fail its first
          task — redeploy with model credentials to fix it.
        </p>
      )}
      {!result.egressRestricted && (
        <p className="text-xs text-muted-foreground">
          <Badge>note</Badge> The sidecar reaches model APIs, git remotes and
          package registries directly; egress is not restricted.
        </p>
      )}
    </div>
  );
}

/**
 * The Ingress host, when there is one. It is the name an operator has to create
 * a DNS record for, and captain deliberately does not create it — so leaving it
 * to be read out of the mutation list would bury the one manual step.
 */
function RouteRow({ result }: { result: DeployResult }) {
  if (!result.route) return null;
  return (
    <>
      <dt className="text-muted-foreground">Route</dt>
      <dd className="font-mono break-all">
        {result.route}
        {result.routeClass && (
          <span className="ml-density-2 font-sans text-muted-foreground">
            via {result.routeClass}
          </span>
        )}
      </dd>
    </>
  );
}

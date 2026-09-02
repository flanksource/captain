import { Button } from "@flanksource/clicky-ui/components";
import { UiRefresh } from "@flanksource/clicky-ui/icons";
import { StateMessage } from "./StateMessage";
import { WhoamiTopology } from "./WhoamiTopology";
import { useWhoamiCatalog } from "./whoamiCatalog";

export function WhoamiPage() {
  const query = useWhoamiCatalog();

  return (
    <div className="h-full overflow-auto">
      <div className="mx-auto flex w-full max-w-7xl flex-col gap-density-6 p-density-4 md:p-density-6">
        <header className="flex flex-wrap items-start justify-between gap-density-3">
          <div className="max-w-4xl space-y-density-2">
            <p className="text-xs font-semibold uppercase tracking-wide text-primary">Captain · /whoami</p>
            <h1 className="text-2xl font-semibold tracking-tight">Capability topology</h1>
            <p className="text-sm text-muted-foreground">
              Browse providers, runtime modes, and models in one hierarchy. Each level has an independent
              availability checkbox, so provider policy, runtime pairs, and individual model exclusions remain visible.
            </p>
          </div>
          <Button
            size="sm"
            variant="outline"
            disabled={query.isFetching}
            onClick={() => void query.refetch()}
          >
            <UiRefresh className={query.isFetching ? "animate-spin" : undefined} />
            Refresh catalog
          </Button>
        </header>

        {query.isLoading ? (
          <StateMessage>Probing runtimes and model catalogs...</StateMessage>
        ) : query.error ? (
          <StateMessage tone="error">{errorMessage(query.error)}</StateMessage>
        ) : query.data ? (
          <WhoamiTopology result={query.data} onRefresh={async () => { await query.refetch(); }} />
        ) : (
          <StateMessage tone="error">Whoami returned no result.</StateMessage>
        )}
      </div>
    </div>
  );
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

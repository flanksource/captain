import type { NamedPrompt } from "./promptNames";
import { errorMessage } from "./promptWorkbenchApi";

export function ExecutedPromptView({
  name,
  prompt,
  loading,
  error,
}: {
  name: string;
  prompt?: NamedPrompt;
  loading: boolean;
  error: unknown;
}) {
  return (
    <section className="mx-auto max-w-2xl space-y-density-4 p-density-5">
      <div>
        <div className="text-xs font-medium uppercase text-muted-foreground">Executed prompt</div>
        <h2 className="text-xl font-semibold">{name}</h2>
      </div>
      {loading ? (
        <p className="text-sm text-muted-foreground">Loading latest execution...</p>
      ) : error ? (
        <p role="alert" className="text-sm text-destructive">{errorMessage(error)}</p>
      ) : !prompt ? (
        <p role="alert" className="text-sm text-muted-foreground">
          No stored execution with this name exists in the selected database context.
        </p>
      ) : (
        <div className="space-y-density-3 rounded-md border border-border bg-card p-density-4 text-sm">
          <div className="font-medium">Latest execution</div>
          <dl className="grid grid-cols-[auto_1fr] gap-x-density-4 gap-y-density-2">
            <dt className="text-muted-foreground">Consumer</dt>
            <dd>{prompt.origin || "Unknown origin"}</dd>
            <dt className="text-muted-foreground">Queued</dt>
            <dd><time dateTime={prompt.queuedAt}>{new Date(prompt.queuedAt).toLocaleString()}</time></dd>
          </dl>
          <a
            className="inline-block font-medium text-primary underline underline-offset-2"
            href={`/sessions/${encodeURIComponent(prompt.sessionId)}`}
          >
            View latest session
          </a>
        </div>
      )}
    </section>
  );
}

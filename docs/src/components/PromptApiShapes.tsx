import { Badge, CodeBlock, JsonView, KeyValueList, MethodBadge } from "@flanksource/clicky-ui/data";
import { ClickyProviders } from "./ClickyProviders";

const promptSummary = {
  id: "embedded\\u0000embedded\\u0000testdata/commit.prompt",
  name: "commit",
  sourceKind: "embedded",
  source: "Embedded examples",
  relPath: "testdata/commit.prompt",
  writable: false,
  model: "claude-sonnet-4-6",
  variables: [{ name: "diff", type: "string" }],
};

const renderResult = {
  id: "local\\u0000a18f2c431efa\\u0000commit.prompt",
  name: "commit",
  model: "claude-sonnet-4-6",
  backend: "anthropic",
  user: "Diff:\\n...",
  system: "You write Conventional Commit messages.",
  validationError: "",
  input: {
    prompt: {
      source: "commit.prompt",
      user: "Diff:\\n...",
      system: "You write Conventional Commit messages.",
    },
    budget: { maxTokens: 1024, timeout: "2h" },
  },
};

export default function PromptApiShapes() {
  return (
    <ClickyProviders>
      <section className="my-6 grid gap-4 xl:grid-cols-[minmax(0,0.9fr)_minmax(0,1.1fr)]">
        <div className="rounded-md border border-border bg-card p-4">
          <div className="mb-3 flex flex-wrap items-center gap-2">
            <MethodBadge method="GET" />
            <Badge variant="outline" tone="neutral" size="sm">
              /api/v1/prompts
            </Badge>
          </div>
          <KeyValueList
            items={[
              { key: "summary", label: "List item", value: "PromptSummary" },
              { key: "detail", label: "Detail item", value: "PromptDetail adds content and schema metadata" },
              { key: "sources", label: "Sources", value: "embedded, configured local dirs, implicit .captain/prompts" },
            ]}
          />
          <div className="mt-4 overflow-auto rounded-md border border-border bg-muted/20 p-3">
            <JsonView data={promptSummary} defaultOpenDepth={2} />
          </div>
        </div>
        <div className="rounded-md border border-border bg-card p-4">
          <div className="mb-3 flex flex-wrap items-center gap-2">
            <MethodBadge method="POST" />
            <Badge variant="outline" tone="neutral" size="sm">
              render / run
            </Badge>
          </div>
          <CodeBlock language="json" source={JSON.stringify(renderResult, null, 2)} />
        </div>
      </section>
    </ClickyProviders>
  );
}


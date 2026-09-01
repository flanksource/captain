import { Badge, CodeBlock, JsonView, KeyValueList, MethodBadge } from "@flanksource/clicky-ui/data";
import { ClickyProviders } from "./ClickyProviders";

const commitInputSchema = {
  type: "object",
  additionalProperties: false,
  required: ["patch", "maxBodyLines"],
  properties: {
    patch: { type: "string", description: "Git patch to summarize" },
    maxBodyLines: {
      type: "integer",
      description: "Maximum commit-message body lines; zero omits the cap",
    },
  },
};

const commitOutputSchema = {
  type: "object",
  additionalProperties: false,
  required: ["type", "subject"],
  properties: {
    type: {
      type: "string",
      description: "Conventional commit type: feat|fix|perf|refactor|test|docs|build|ci|chore|revert",
    },
    scope: { type: "string", description: "Optional scope, e.g. db, api, fe, kubernetes" },
    subject: {
      type: "string",
      description: "Imperative subject line, max 100 chars, no trailing period",
    },
    body: { type: "string", description: "Optional body explaining why and impact" },
  },
};

const promptSummary = {
  id: "embedded\\u0000embedded\\u0000testdata/commit.prompt",
  name: "commit",
  sourceKind: "embedded",
  source: "Embedded examples",
  relPath: "testdata/commit.prompt",
  writable: false,
  model: "claude-sonnet-4-6",
  variables: [
    {
      name: "maxBodyLines",
      type: "integer",
      description: "Maximum commit-message body lines; zero omits the cap",
      required: true,
    },
    { name: "patch", type: "string", description: "Git patch to summarize", required: true },
  ],
};

const renderResult = {
  id: "local\\u0000a18f2c431efa\\u0000commit.prompt",
  name: "commit",
  model: "claude-sonnet-4-6",
  mode: "agent",
  user: "DIFF INPUT:\\n\\n...\\n\\nREQUIREMENTS:\\n- type: one of feat|fix|perf|refactor|test|docs|build|ci|chore|revert\\n...",
  system: "You are a commit message generator. Analyze the diff below and produce a Conventional Commit message.",
  validationError: "",
  input: {
    prompt: {
      source: "commit.prompt",
      user: "DIFF INPUT:\\n\\n...\\n\\nREQUIREMENTS:\\n- type: one of feat|fix|perf|refactor|test|docs|build|ci|chore|revert\\n...",
      system: "You are a commit message generator. Analyze the diff below and produce a Conventional Commit message.",
      schemaJSON: commitOutputSchema,
    },
    budget: { maxTokens: 1024, timeout: "2h" },
  },
  inputSchema: commitInputSchema,
  outputSchema: commitOutputSchema,
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

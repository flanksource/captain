import { useMemo, useState } from "react";
import { InputField, SegmentedControl } from "@flanksource/clicky-ui/components";
import { Badge, KeyValueList } from "@flanksource/clicky-ui/data";
import { ClickyProviders } from "./ClickyProviders";

type Stage = "load" | "render" | "fold" | "overlay";

const stages: Array<{
  id: Stage;
  label: string;
  description: string;
  owner: string;
  output: string;
}> = [
  {
    id: "load",
    label: "Load",
    description: "Read inline, file, stdin, embedded, or local .prompt source.",
    owner: "prompt.Load, prompt.LoadFile, prompt.LoadFS",
    output: "Template source and source name",
  },
  {
    id: "render",
    label: "Render",
    description: "dotprompt renders frontmatter and role-marked body content.",
    owner: "Template.Render",
    output: "System and user message text",
  },
  {
    id: "fold",
    label: "Fold spec",
    description: "Spec-native frontmatter is decoded into ai.Request/api.Spec.",
    owner: "decodeSpecFrontmatter",
    output: "Typed request plus ai.Config",
  },
  {
    id: "overlay",
    label: "Overlay",
    description: "CLI flags, saved defaults, and runtime spec edits resolve the final request.",
    owner: "overlayCLI, overlayRuntimeSpec",
    output: "Provider-ready request",
  },
];

export default function PromptPipeline() {
  const [stage, setStage] = useState<Stage>("render");
  const [filter, setFilter] = useState("");
  const selected = stages.find((item) => item.id === stage) ?? stages[0];
  const visibleStages = useMemo(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return stages;
    return stages.filter((item) =>
      `${item.label} ${item.description} ${item.owner} ${item.output}`
        .toLowerCase()
        .includes(q),
    );
  }, [filter]);

  return (
    <ClickyProviders>
      <section className="my-6 rounded-md border border-border bg-card p-4">
        <div className="mb-4 flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
          <div>
            <div className="text-sm font-semibold">Prompt render pipeline</div>
            <div className="text-sm text-muted-foreground">
              Static map of the engine path before provider execution.
            </div>
          </div>
          <InputField
            value={filter}
            onChange={setFilter}
            placeholder="Filter stages"
            shortcut="/"
            className="w-full md:w-64"
          />
        </div>
        <SegmentedControl
          value={stage}
          onChange={setStage}
          size="lg"
          wrap
          aria-label="Prompt pipeline stage"
          options={visibleStages.map((item) => ({
            id: item.id,
            label: item.label,
            description: item.output,
          }))}
        />
        <div className="mt-4 grid gap-3 lg:grid-cols-[minmax(0,1fr)_minmax(18rem,24rem)]">
          <KeyValueList
            items={[
              { key: "stage", label: "Stage", value: selected.label },
              { key: "owner", label: "Source seam", value: selected.owner },
              { key: "output", label: "Output", value: selected.output },
            ]}
          />
          <div className="rounded-md border border-border bg-muted/30 p-3">
            <Badge variant="outline" tone="info" size="sm">
              {selected.id}
            </Badge>
            <p className="mt-2 text-sm text-muted-foreground">{selected.description}</p>
          </div>
        </div>
      </section>
    </ClickyProviders>
  );
}


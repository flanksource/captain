import { useState } from "react";
import { SpecRuntimeEditor, type AISpecRuntimeValue } from "@flanksource/clicky-ui/ai";
import type { ToolMeta } from "@flanksource/clicky-ui/ai";
import { ClickyProviders } from "./ClickyProviders";

const tools: ToolMeta[] = [
  {
    name: "Read",
    label: "Read",
    group: "Files",
    defaultPermission: "ask",
    description: "Read files from the workspace.",
  },
  {
    name: "Edit",
    label: "Edit",
    group: "Files",
    defaultPermission: "ask",
    description: "Apply targeted edits.",
  },
  {
    name: "Bash",
    label: "Bash",
    group: "Shell",
    defaultPermission: "ask",
    description: "Run shell commands.",
  },
  {
    name: "WebSearch",
    label: "Web search",
    group: "Web",
    defaultPermission: "off",
    description: "Search external documentation.",
  },
];

const initialValue: AISpecRuntimeValue = {
  model: "claude-sonnet-4-6",
  backend: "anthropic",
  effort: "medium",
  temperature: 0.2,
  budget: {
    maxTokens: 5000,
    maxTurns: 3,
    timeout: "2h",
    cost: 1,
  },
  prompt: {
    system: "You are a careful refactoring assistant.",
    user: "Refactor: {{target}}",
  },
  permissions: {
    mode: "acceptEdits",
    presets: ["edit"],
    tools: {
      Read: "allow",
      Edit: "allow",
      Bash: "ask",
      WebSearch: "deny",
    },
    mcp: {
      servers: [],
    },
  },
  memory: {
    skipUser: true,
  },
  setup: {
    cwd: ".",
  },
};

export default function RuntimeSpecDemo() {
  const [value, setValue] = useState<AISpecRuntimeValue>(initialValue);

  return (
    <ClickyProviders>
      <div className="my-6 overflow-hidden rounded-md border border-border bg-card">
        <div className="border-b border-border px-4 py-3">
          <div className="text-sm font-semibold">Static runtime spec example</div>
          <div className="text-sm text-muted-foreground">
            This mirrors the runtime overlay shape. It does not call a Captain API.
          </div>
        </div>
        <div className="max-h-[46rem] overflow-auto">
          <SpecRuntimeEditor
            value={value}
            onChange={setValue}
            tools={tools}
            title="Prompt runtime"
            eyebrow="docs example"
          />
        </div>
      </div>
    </ClickyProviders>
  );
}

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { RuntimePreset, RuntimeProfile } from "@flanksource/clicky-ui/ai";
import { PromptDetailPane } from "./PromptDetailPane";
import { SCRATCH_PROMPT } from "./promptDetailState";

type RunEditorProps = {
  presets?: RuntimePreset[];
  profiles?: RuntimeProfile[];
};

const runEditorProps: RunEditorProps[] = [];

vi.mock("@flanksource/clicky-ui/ai", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@flanksource/clicky-ui/ai")>()),
  PromptRunEditor: (props: RunEditorProps) => {
    runEditorProps.push(props);
    return (
      <div>
        {props.presets ? (
          <div aria-label="Runtime presets">
            {props.presets.map((preset) => preset.name).join(", ")}
          </div>
        ) : null}
      </div>
    );
  },
}));

const PRESET: RuntimePreset = {
  id: "preset-plan",
  name: "Plan",
  scope: "context",
  spec: {},
};

afterEach(() => {
  cleanup();
  runEditorProps.length = 0;
});

function baseProps() {
  return {
    detail: { ...SCRATCH_PROMPT, run: {} },
    hasSelection: true,
    loading: false,
    error: undefined,
    tab: "runner" as const,
    onTabChange: vi.fn(),
    draft: "",
    draftDirty: false,
    onDraftChange: vi.fn(),
    onSchemaValidityChange: vi.fn(),
    variablesValid: true,
    onVariablesValidityChange: vi.fn(),
    runRequest: {},
    onRunRequestChange: vi.fn(),
    models: [],
    tools: [],
    onEditBatch: vi.fn(),
    onSelectRun: vi.fn(),
    onPreview: vi.fn(),
    onRun: vi.fn(),
    previewLoading: false,
    runLoading: false,
    previewEnabled: true,
    runEnabled: true,
  };
}

describe("PromptDetailPane runtime presets", () => {
  it("renders the runtime presets picker when presets are loaded without profiles", () => {
    render(<PromptDetailPane {...baseProps()} presets={[PRESET]} />);

    expect(screen.getByLabelText("Runtime presets")).toHaveTextContent(
      "Plan",
    );
    expect(runEditorProps[0]?.presets).toEqual([PRESET]);
    expect(runEditorProps[0]?.profiles).toBeUndefined();
  });

  it("omits the presets prop entirely when no presets are loaded", () => {
    render(<PromptDetailPane {...baseProps()} />);

    expect(screen.queryByLabelText("Runtime presets")).not.toBeInTheDocument();
    expect(runEditorProps[0]).not.toHaveProperty("presets");
  });
});

describe("PromptDetailPane fan-out", () => {
  const source = "---\n# keep this comment\nmodel: api:sonnet\n---\nReview {{item.name}}.\n";
  const detail = { ...SCRATCH_PROMPT, id: "review", writable: true, content: source };

  it("applies a script and concurrency to the prompt draft", () => {
    const props = baseProps();
    render(<PromptDetailPane {...props} detail={detail} draft={source} tab="fanout" />);

    fireEvent.change(screen.getByRole("textbox", { name: /^Script/ }), { target: { value: "printf '[]'" } });
    fireEvent.change(screen.getByLabelText("Concurrent sessions (optional)"), { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply to draft" }));

    expect(props.onDraftChange).toHaveBeenCalledWith(expect.stringMatching(
      /# keep this comment[\s\S]*fanout:[\s\S]*exec: printf '\[\]'[\s\S]*concurrency: 2[\s\S]*Review \{\{item.name\}\}\./,
    ));
  });

  it("keeps invalid fan-out settings out of the draft", () => {
    const props = baseProps();
    render(<PromptDetailPane {...props} detail={detail} draft={source} tab="fanout" />);

    fireEvent.change(screen.getByLabelText("Concurrent sessions (optional)"), { target: { value: "1.5" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply to draft" }));

    expect(screen.getByText("Enter a script.")).toBeInTheDocument();
    expect(screen.getByText("Enter a whole number of at least 1.")).toBeInTheDocument();
    expect(props.onDraftChange).not.toHaveBeenCalled();
  });
});

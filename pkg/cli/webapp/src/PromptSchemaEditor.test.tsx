import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PromptSchemaEditor } from "./PromptSchemaEditor";

const monacoModels = vi.hoisted(
  () => new Map<string, { value: string; dispose: () => void }>(),
);

vi.mock("@flanksource/clicky-ui/monaco", () => ({
  MonacoEditor: ({
    value,
    onChange,
    path,
    onMount,
  }: {
    value: string;
    onChange: (value: string) => void;
    path: string;
    onMount?: (editor: {
      getModel: () => { value: string; dispose: () => void };
    }) => void;
  }) => {
    const model = monacoModels.get(path) ?? {
      value,
      dispose: () => monacoModels.delete(path),
    };
    monacoModels.set(path, model);
    onMount?.({ getModel: () => model });
    return (
      <textarea
        data-path={path}
        value={model.value}
        onChange={(event) => {
          model.value = event.target.value;
          onChange(event.target.value);
        }}
      />
    );
  },
}));

afterEach(() => {
  cleanup();
  monacoModels.clear();
});

const SOURCE = `---
input:
  schema:
    topic: string
output:
  schema:
    type: object
---
Write about {{topic}}.
`;

describe("PromptSchemaEditor", () => {
  it("renders separate editors for the prompt file input and output schemas", async () => {
    render(
      <PromptSchemaEditor
        promptId="review.prompt"
        source={SOURCE}
        onSourceChange={vi.fn()}
        onValidityChange={vi.fn()}
      />,
    );

    expect(screen.getByText("Input schema")).toBeInTheDocument();
    expect(screen.getByText("Output schema")).toBeInTheDocument();
    const editors = await screen.findAllByRole("textbox");
    expect(editors).toHaveLength(2);
    expect((editors[0] as HTMLTextAreaElement).value).toContain('"topic"');
    expect((editors[1] as HTMLTextAreaElement).value).toContain('"type"');
  });

  it("writes a valid editor change back into the prompt source", async () => {
    const onSourceChange = vi.fn();
    const onValidityChange = vi.fn();
    render(
      <PromptSchemaEditor
        promptId="review.prompt"
        source={SOURCE}
        onSourceChange={onSourceChange}
        onValidityChange={onValidityChange}
      />,
    );

    fireEvent.change((await screen.findAllByRole("textbox"))[0]!, {
      target: {
        value: JSON.stringify({
          type: "object",
          properties: { diff: { type: "string" } },
        }),
      },
    });

    expect(onValidityChange).toHaveBeenLastCalledWith("input", true);
    expect(onSourceChange).toHaveBeenCalledWith(
      expect.stringContaining("diff:"),
    );
    expect(onSourceChange).toHaveBeenCalledWith(
      expect.stringContaining("output:"),
    );
  });

  it("reports invalid JSON and does not replace the last valid source", async () => {
    const onSourceChange = vi.fn();
    const onValidityChange = vi.fn();
    render(
      <PromptSchemaEditor
        promptId="review.prompt"
        source={SOURCE}
        onSourceChange={onSourceChange}
        onValidityChange={onValidityChange}
      />,
    );

    fireEvent.change((await screen.findAllByRole("textbox"))[0]!, {
      target: { value: "{" },
    });

    expect(screen.getByRole("alert")).toHaveTextContent("JSON");
    expect(onValidityChange).toHaveBeenLastCalledWith("input", false);
    expect(onSourceChange).not.toHaveBeenCalled();
  });

  it("restores schema validity when the editor remounts from valid source", async () => {
    const onValidityChange = vi.fn();
    const props = {
      promptId: "review.prompt",
      source: SOURCE,
      onSourceChange: vi.fn(),
      onValidityChange,
    };
    const view = render(<PromptSchemaEditor {...props} />);

    fireEvent.change((await screen.findAllByRole("textbox"))[0]!, {
      target: { value: "{" },
    });
    expect(onValidityChange).toHaveBeenLastCalledWith("input", false);

    view.unmount();
    onValidityChange.mockClear();
    render(<PromptSchemaEditor {...props} />);

    await waitFor(() => {
      expect(onValidityChange).toHaveBeenCalledWith("input", true);
      expect(onValidityChange).toHaveBeenCalledWith("output", true);
    });
    expect((await screen.findAllByRole("textbox"))[0]).toHaveValue(
      JSON.stringify({ topic: "string" }, null, 2),
    );
  });
});

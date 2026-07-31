import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PromptWriteAction, PromptWriteModal } from "./PromptWriteModal";

vi.mock("@flanksource/clicky-ui/mdx-editor", () => ({
  MdxEditorField: ({
    value,
    onChange,
  }: {
    value: string;
    onChange?: (value: string) => void;
  }) => (
    <textarea
      aria-label="Prompt Source"
      value={value}
      onChange={(event) => onChange?.(event.target.value)}
    />
  ),
}));

afterEach(() => cleanup());

const SOURCE = `---
name: Commit Message
---
{{role "user"}}
Summarize the patch.
`;

describe("PromptWriteModal", () => {
  it.each([
    { writable: true, label: "Save" },
    { writable: false, label: "Save as…" },
  ])("renders the $label action for writable=$writable", ({ writable, label }) => {
    render(
      <PromptWriteAction
        writable={writable}
        disabled={false}
        loading={false}
        onClick={vi.fn()}
      />,
    );

    expect(screen.getByRole("button", { name: label })).toBeInTheDocument();
  });

  it("preserves copied source while editing a save-as destination", async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(
      <PromptWriteModal
        open
        mode="save-as"
        sources={[{ id: "local-1", label: "/work/prompts" }]}
        initialName="Commit Message"
        initialContent={SOURCE}
        onClose={vi.fn()}
        onSubmit={onSubmit}
      />,
    );

    expect(screen.getByRole("dialog", { name: "Save Prompt As" })).toBeInTheDocument();
    expect(screen.getByLabelText("Name")).toHaveValue("Commit Message");
    expect(screen.getByLabelText("Prompt Source")).toHaveValue(SOURCE);

    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Commit Copy" },
    });
    fireEvent.change(screen.getByLabelText("Path"), {
      target: { value: "copies/commit.prompt" },
    });

    expect(screen.getByLabelText("Prompt Source")).toHaveValue(SOURCE);
    fireEvent.click(screen.getByRole("button", { name: "Save As" }));

    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({
        target: "local-1",
        name: "Commit Copy",
        relPath: "copies/commit.prompt",
        content: SOURCE,
      }),
    );
  });

  it("keeps a failed save-as open and displays the create error", async () => {
    render(
      <PromptWriteModal
        open
        mode="save-as"
        sources={[]}
        initialName="Commit Message"
        initialContent={SOURCE}
        onClose={vi.fn()}
        onSubmit={vi.fn().mockRejectedValue(new Error("prompt commit-message.prompt already exists"))}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Save As" }));

    expect(
      await screen.findByText("prompt commit-message.prompt already exists"),
    ).toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "Save Prompt As" })).toBeInTheDocument();
  });
});

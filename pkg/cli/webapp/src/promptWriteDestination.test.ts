import { describe, expect, it } from "vitest";
import {
  promptWriteDestination,
  promptWriteRelPath,
  slugPromptName,
} from "./promptWriteDestination";

const LOCAL = { id: "883fbbc4b6ff", label: "/work/.captain/prompts", root: "/work/.captain/prompts" };

describe("promptWriteRelPath", () => {
  it.each([
    { relPath: "", name: "Diff Review!", expected: "diff-review.prompt" },
    { relPath: "", name: "  UI/UX  audit ", expected: "ui-ux-audit.prompt" },
    { relPath: "copies/commit", name: "ignored", expected: "copies/commit.prompt" },
    { relPath: "copies/commit.prompt", name: "ignored", expected: "copies/commit.prompt" },
    { relPath: "", name: "!!!", expected: "" },
  ])("mirrors the server for relPath=$relPath name=$name", ({ relPath, name, expected }) => {
    expect(promptWriteRelPath(relPath, name)).toBe(expected);
  });

  it("slugs the way captain does", () => {
    expect(slugPromptName("Commit Message v2")).toBe("commit-message-v2");
  });
});

describe("promptWriteDestination", () => {
  it("shows the full path inside the chosen source", () => {
    expect(promptWriteDestination(LOCAL, "", "Diff Review")).toBe(
      "/work/.captain/prompts/diff-review.prompt",
    );
  });

  it("falls back to the source label when the root is unknown", () => {
    expect(promptWriteDestination({ id: "x", label: "Examples" }, "a/b", "")).toBe(
      "Examples/a/b.prompt",
    );
  });

  it("shows only the file when no source is selected", () => {
    expect(promptWriteDestination(undefined, "", "Diff Review")).toBe("diff-review.prompt");
  });

  it("is empty until a name or path exists", () => {
    expect(promptWriteDestination(LOCAL, "", "")).toBe("");
  });
});

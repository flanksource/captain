import { describe, expect, it } from "vitest";
import {
  parseSchemaEditorValue,
  readPromptSchemas,
  updatePromptSchemaSource,
} from "./promptSchemaSource";

const SOURCE = `---
# keep this comment
model: claude-sonnet-4-6
input:
  default:
    topic: captain
  schema:
    topic: string
output:
  schema:
    type: object
    properties:
      title:
        type: string
---
{{role "user"}}
Write about {{topic}}.
`;

describe("updatePromptSchemaSource", () => {
  it("updates only the selected prompt-file schema", () => {
    const updated = updatePromptSchemaSource(SOURCE, "input", {
      type: "object",
      properties: { diff: { type: "string" } },
      required: ["diff"],
    });

    expect(updated).toContain("# keep this comment");
    expect(updated).toContain("topic: captain");
    expect(updated).toContain("diff:");
    expect(updated).toContain("required:");
    expect(updated).toContain("title:");
    expect(updated).toContain(
      '{{role "user"}}\nWrite about {{topic}}.\n',
    );
  });

  it("adds frontmatter when a body-only prompt gains a schema", () => {
    const updated = updatePromptSchemaSource("Review {{diff}}.\n", "output", {
      type: "object",
      properties: { summary: { type: "string" } },
    });

    expect(updated).toMatch(/^---\noutput:\n  schema:/);
    expect(updated).toContain("summary:");
    expect(updated).toMatch(/---\nReview \{\{diff\}\}\.\n$/);
  });

  it("adds a schema to an existing empty frontmatter block", () => {
    const updated = updatePromptSchemaSource(
      "---\n---\nReview {{diff}}.\n",
      "input",
      { type: "object", properties: { diff: { type: "string" } } },
    );

    expect(updated).toMatch(/^---\ninput:\n  schema:/);
    expect(updated).toContain("diff:");
    expect(updated.match(/^---$/gm)).toHaveLength(2);
  });

  it("preserves leading prompt comments around frontmatter", () => {
    const source = `# prompt metadata
# second comment
---
model: claude-sonnet-4-6
---
Review {{diff}}.
`;
    const updated = updatePromptSchemaSource(source, "input", {
      type: "object",
      properties: { diff: { type: "string" } },
    });

    expect(updated).toMatch(/^# prompt metadata\n# second comment\n---\n/);
    expect(updated).toContain("model: claude-sonnet-4-6");
    expect(updated).toContain("Review {{diff}}.");
    expect(updated.match(/^---$/gm)).toHaveLength(2);
  });

  it("preserves carriage-return-delimited comments around frontmatter", () => {
    const source =
      "# prompt metadata\r---\rmodel: claude-sonnet-4-6\r---\rReview {{diff}}.\r";
    const updated = updatePromptSchemaSource(source, "input", {
      type: "object",
      properties: { diff: { type: "string" } },
    });

    expect(updated).toMatch(/^# prompt metadata\r---\n/);
    expect(updated).toContain("model: claude-sonnet-4-6");
    expect(updated).toContain("Review {{diff}}.\r");
  });

  it("removes the selected schema without removing sibling frontmatter", () => {
    const updated = updatePromptSchemaSource(SOURCE, "output", undefined);

    expect(updated).not.toContain("output:");
    expect(updated).toContain("input:");
    expect(updated).toContain("model: claude-sonnet-4-6");
  });
});

describe("readPromptSchemas", () => {
  it("reads both schemas from the current prompt source", () => {
    expect(readPromptSchemas(SOURCE)).toEqual({
      input: { topic: "string" },
      output: {
        type: "object",
        properties: { title: { type: "string" } },
      },
    });
  });
});

describe("parseSchemaEditorValue", () => {
  it("accepts a JSON object", () => {
    expect(parseSchemaEditorValue('{"type":"object"}')).toEqual({
      type: "object",
    });
  });

  it.each(["", "null", "[]", '"string"'])(
    "rejects a non-object schema value %j",
    (value) => {
      expect(() => parseSchemaEditorValue(value)).toThrow(
        "Schema must be a JSON object.",
      );
    },
  );
});

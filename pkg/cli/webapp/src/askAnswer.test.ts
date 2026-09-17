import { questionsFromToolInput } from "@flanksource/clicky-ui/ai";
import { describe, expect, it } from "vitest";
import { askAnswerTools, formatAskAnswer, type AwaitingInput } from "./askAnswer";

const awaiting: AwaitingInput = {
  origin: "envelope",
  summary: "Blocked on scope.",
  messageId: "ask",
  toolCallId: "ask-call",
  questions: [
    {
      text: "Which work should I implement?",
      context: "They are disjoint.",
      options: ["The plan", "The todo", "Both"],
      optionDescriptions: { "The plan": "Rail layout" },
    },
    { text: "Which checks should run?", multiSelect: true, options: ["Unit", "E2E"] },
  ],
};

describe("formatAskAnswer", () => {
  it("numbers each question with the answer chosen for it", () => {
    expect(formatAskAnswer(awaiting, {
      allow: true,
      answers: { "Which work should I implement?": "The plan", "Which checks should run?": ["Unit", "E2E"] },
    })).toBe(
      "Answers:\n1. Which work should I implement?\n→ The plan\n2. Which checks should run?\n→ Unit, E2E",
    );
  });

  it("marks a question left unanswered so the agent does not guess", () => {
    expect(formatAskAnswer(awaiting, { allow: true, answers: { "Which checks should run?": ["Unit"] } })).toBe(
      "Answers:\n1. Which work should I implement?\n→ (no answer)\n2. Which checks should run?\n→ Unit",
    );
  });

  it("refuses to send when nothing was answered", () => {
    expect(() => formatAskAnswer(awaiting, { allow: true, answers: { "Which checks should run?": [] } }))
      .toThrow("Choose or write an answer before sending.");
  });

  it("sends a written message in place of the chosen answers", () => {
    expect(formatAskAnswer(awaiting, {
      allow: true, message: "Do the plan, skip E2E.", answers: { "Which work should I implement?": "The plan" },
    })).toBe("Do the plan, skip E2E.");
  });

  it.each([
    [undefined, "I won't answer these questions."],
    ["Wrong todo, close it.", "I won't answer these questions: Wrong todo, close it."],
  ])("declines the questions with message %s", (message, want) => {
    expect(formatAskAnswer(awaiting, { allow: false, ...(message ? { message } : {}) })).toBe(want);
  });
});

describe("askAnswerTools", () => {
  it("puts an envelope's questions forward as a new pending AskUserQuestion row", () => {
    expect(askAnswerTools(awaiting)).toEqual([{
      tool: "AskUserQuestion",
      input: { questions: expect.any(Array) },
    }]);
  });

  it("answers a native AskUserQuestion on its own transcript row", () => {
    expect(askAnswerTools({ ...awaiting, origin: "ask-user-question" })).toEqual([{
      tool: "AskUserQuestion",
      input: { questions: expect.any(Array) },
      toolCallId: "ask-call",
    }]);
  });

  it("lets the answer form offer each option's description and pick several where the agent allows it", () => {
    const [tool] = askAnswerTools(awaiting);

    expect(questionsFromToolInput(tool?.input)).toEqual([
      {
        id: "1", text: "Which work should I implement?", context: "They are disjoint.", multiSelect: false,
        options: [
          { value: "The plan", label: "The plan", description: "Rail layout" },
          { value: "The todo", label: "The todo" },
          { value: "Both", label: "Both" },
        ],
      },
      {
        id: "2", text: "Which checks should run?", multiSelect: true,
        options: [{ value: "Unit", label: "Unit" }, { value: "E2E", label: "E2E" }],
      },
    ]);
  });

  it("puts nothing forward when nothing awaits input", () => {
    expect(askAnswerTools(undefined)).toEqual([]);
  });
});

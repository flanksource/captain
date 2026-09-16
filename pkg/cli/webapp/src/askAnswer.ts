import type {
  SessionPendingTool,
  SessionToolDecision,
} from "@flanksource/clicky-ui/ai";

/** Questions a session's last turn left for a person to answer. */
export type AwaitingInput = {
  origin: "envelope" | "ask-user-question";
  summary?: string;
  questions: {
    text: string;
    context?: string;
    options?: string[];
    /** Descriptions of the options the agent described, by label. */
    optionDescriptions?: Record<string, string>;
    multiSelect?: boolean;
  }[];
  messageId?: string;
  toolCallId?: string;
};

/** The questions a session awaits, put forward as one pending AskUserQuestion so
 *  the transcript renders its answer form. A native AskUserQuestion is answered
 *  on its own row; an envelope's questions get a new row, since matching the
 *  StructuredOutput call would put the form on a row that renders none. */
export function askAnswerTools(awaiting?: AwaitingInput): SessionPendingTool[] {
  if (!awaiting) return [];
  const questions = awaiting.questions.map(({ optionDescriptions, options, ...question }) => ({
    ...question,
    options: options?.map((label) => ({ label, description: optionDescriptions?.[label] })),
  }));
  return [{
    tool: "AskUserQuestion",
    input: { questions },
    ...(awaiting.origin === "ask-user-question" && awaiting.toolCallId
      ? { toolCallId: awaiting.toolCallId }
      : {}),
  }];
}

/** The user turn that answers (or declines) the questions a session awaits. */
export function formatAskAnswer(
  awaiting: AwaitingInput,
  decision: Pick<SessionToolDecision, "allow" | "message" | "answers">,
): string {
  const message = decision.message?.trim();
  if (!decision.allow) {
    return message
      ? `I won't answer these questions: ${message}`
      : "I won't answer these questions.";
  }
  if (message) return message;
  const answers = awaiting.questions.map((question) =>
    answerText(decision.answers?.[question.text]),
  );
  if (answers.every((answer) => answer === undefined)) {
    throw new Error("Choose or write an answer before sending.");
  }
  const lines = awaiting.questions.map(
    (question, index) =>
      `${index + 1}. ${question.text}\n→ ${answers[index] ?? "(no answer)"}`,
  );
  return `Answers:\n${lines.join("\n")}`;
}

function answerText(answer: string | string[] | undefined): string | undefined {
  const text = Array.isArray(answer)
    ? answer.map((value) => value.trim()).filter(Boolean).join(", ")
    : answer?.trim();
  return text ? text : undefined;
}

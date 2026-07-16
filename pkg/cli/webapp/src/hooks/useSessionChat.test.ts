import type { SessionUIMessage } from "@flanksource/clicky-ui/ai";
import { describe, expect, it } from "vitest";
import { mergeSessionMessages } from "./useSessionChat";

describe("mergeSessionMessages", () => {
  it("reconciles an optimistic user message by exact id", () => {
    const optimistic: SessionUIMessage = {
      id: "message-1",
      role: "user",
      parts: [{ type: "text", text: "optimistic" }],
    };
    const accepted: SessionUIMessage = {
      id: "message-1",
      role: "user",
      parts: [{ type: "text", text: "accepted" }],
    };

    const merged = mergeSessionMessages([optimistic], [accepted]);

    expect(merged).toEqual([accepted]);
  });
});

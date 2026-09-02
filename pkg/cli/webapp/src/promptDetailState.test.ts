import { describe, expect, it } from "vitest";
import type { PromptDetail } from "./promptData";
import {
  EMPTY_RUN_REQUEST,
  SCRATCH_PROMPT,
  anyPromptDirty,
  isPromptDirty,
  promptDetailReducer,
  promptDetailStateFor,
  type PromptDetailStates,
} from "./promptDetailState";

function detail(id: string, content: string): PromptDetail {
  return {
    id,
    name: id,
    sourceKind: "local",
    sourceId: "local",
    source: "/work/prompts",
    path: `/work/prompts/${id}.prompt`,
    relPath: `${id}.prompt`,
    writable: true,
    content,
    run: EMPTY_RUN_REQUEST,
  };
}

const alpha = detail("alpha", "alpha v1");
const beta = detail("beta", "beta v1");

describe("promptDetailReducer", () => {
  it("keeps a draft when the user switches to another prompt and back", () => {
    let states: PromptDetailStates = {};
    states = promptDetailReducer(states, { type: "draft", detail: alpha, value: "alpha edited" });
    states = promptDetailReducer(states, { type: "draft", detail: beta, value: "beta edited" });

    const restored = promptDetailStateFor(states, alpha);

    expect(restored.draft).toBe("alpha edited");
    expect(isPromptDirty(restored, alpha)).toBe(true);
    expect(promptDetailStateFor(states, beta).draft).toBe("beta edited");
  });

  it("clears dirtiness once the draft is saved", () => {
    let states: PromptDetailStates = {};
    states = promptDetailReducer(states, { type: "draft", detail: alpha, value: "alpha v2" });
    states = promptDetailReducer(states, { type: "saved", detail: alpha, content: "alpha v2" });

    // Before the refetch lands the detail still carries the old content; the
    // saved slot must not be reset to it.
    const beforeRefetch = promptDetailStateFor(states, alpha);
    expect(beforeRefetch.draft).toBe("alpha v2");
    expect(isPromptDirty(beforeRefetch, alpha)).toBe(false);
    expect(anyPromptDirty(states)).toBe(false);

    const refetched = detail("alpha", "alpha v2");
    const afterRefetch = promptDetailStateFor(states, refetched);
    expect(afterRefetch.draft).toBe("alpha v2");
    expect(isPromptDirty(afterRefetch, refetched)).toBe(false);
  });

  it("adopts content that changed on disk when the draft was untouched", () => {
    const states = promptDetailReducer({}, { type: "run-request", detail: alpha, value: EMPTY_RUN_REQUEST });

    const reloaded = promptDetailStateFor(states, detail("alpha", "alpha v2 from disk"));

    expect(reloaded.draft).toBe("alpha v2 from disk");
    expect(isPromptDirty(reloaded, alpha)).toBe(false);
  });

  it("keeps a dirty draft when the content changed on disk and measures it against the new file", () => {
    const states = promptDetailReducer({}, { type: "draft", detail: alpha, value: "my edit" });

    const reloaded = promptDetailStateFor(states, detail("alpha", "alpha v2 from disk"));

    expect(reloaded.draft).toBe("my edit");
    expect(reloaded.savedContent).toBe("alpha v2 from disk");
    expect(isPromptDirty(reloaded, alpha)).toBe(true);
  });

  it("seeds the run request with the runtime profile the prompt pins, and only then", () => {
    const pinned: PromptDetail = {
      ...detail("alpha", "alpha v1"),
      runtimeProfile: "Review",
      run: { ...EMPTY_RUN_REQUEST, runtimeProfile: "Review" },
    };

    expect(promptDetailStateFor({}, pinned).runRequest).toMatchObject({
      runtimeProfile: "Review",
      spec: EMPTY_RUN_REQUEST.spec,
    });
    expect(promptDetailStateFor({}, alpha).runRequest).not.toHaveProperty("runtimeProfile");
  });

  it("never reports the scratch prompt as dirty", () => {
    const states = promptDetailReducer({}, {
      type: "run-request",
      detail: SCRATCH_PROMPT,
      value: { ...EMPTY_RUN_REQUEST, spec: { prompt: { user: "hi" } } },
    });

    expect(isPromptDirty(promptDetailStateFor(states, SCRATCH_PROMPT), SCRATCH_PROMPT)).toBe(false);
    expect(anyPromptDirty(states)).toBe(false);
  });
});

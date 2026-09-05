import { describe, expect, it } from "vitest";
import type { PromptDetail } from "./promptData";
import {
  EMPTY_RUN_REQUEST,
  SCRATCH_PROMPT,
  anyPromptDirty,
  isPromptDirty,
  promptDetailReducer,
  promptDetailStateFor,
  promptRuntimeForDisplay,
  promptPreviewKey,
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
  it("keeps saved runtime defaults out of the request and preserves explicit profile overrides", () => {
    const seeded: PromptDetail = {
      ...alpha,
      run: { variables: { topic: 'example' }, chat: true, spec: { model: 'saved-model', mode: 'agent', budget: { timeout: '2h' } }, runtimes: [{ model: 'saved-comparison' }] },
    };
    const initial = promptDetailStateFor({}, seeded);
    expect(initial.runRequest).toEqual({ variables: { topic: 'example' }, chat: true, spec: {} });
    const selected = { ...initial.runRequest, runtimeProfile: 'review-profile' };
    let states = promptDetailReducer({}, { type: 'run-request', detail: seeded, value: selected });
    expect(promptDetailStateFor(states, seeded).runRequest).toEqual(selected);
    const explicit = { ...selected, spec: { model: 'saved-model' } };
    states = promptDetailReducer(states, { type: 'run-request', detail: seeded, value: explicit });
    expect(promptDetailStateFor(states, seeded).runRequest).toEqual(explicit);
  });

  it.each(['draft', 'run-request'] as const)('clears stale resolution after a %s edit', type => {
    let states = promptDetailReducer({}, { type: 'preview-result', detail: alpha, key: promptPreviewKey(promptDetailStateFor({}, alpha)), value: { id: alpha.id, name: alpha.name, model: 'previous-model' } });
    states = promptDetailReducer(states, type === 'draft'
      ? { type, detail: alpha, value: 'updated source' }
      : { type, detail: alpha, value: { runtimeProfile: 'new-profile', spec: {} } });
    expect(promptDetailStateFor(states, alpha).previewResult).toBeUndefined();
  });

  it.each(['draft', 'run-request'] as const)('ignores an old preview received after a %s edit', type => {
    const key = promptPreviewKey(promptDetailStateFor({}, alpha));
    let states = promptDetailReducer({}, type === 'draft'
      ? { type, detail: alpha, value: 'updated source' }
      : { type, detail: alpha, value: { runtimeProfile: 'new-profile', spec: {} } });
    states = promptDetailReducer(states, { type: 'preview-result', detail: alpha, key, value: { id: alpha.id, name: alpha.name, model: 'previous-model' } });
    expect(promptDetailStateFor(states, alpha).previewResult).toBeUndefined();
    const latest = { id: alpha.id, name: alpha.name, model: 'updated-model' };
    states = promptDetailReducer(states, { type: 'preview-result', detail: alpha, key: promptPreviewKey(promptDetailStateFor(states, alpha)), value: latest });
    expect(promptDetailStateFor(states, alpha).previewResult).toEqual(latest);
  });

  it('uses resolved profile runtime only for display', () => {
    const request = { runtimeProfile: 'review-profile', spec: {} };
    const runtime = promptRuntimeForDisplay(request, {
      id: alpha.id, name: alpha.name,
      resolution: { spec: { model: 'preset-model', mode: 'cmux' }, constraints: {}, trace: [] },
    });
    expect(runtime).toEqual({ model: 'preset-model', mode: 'cmux' });
    expect(request).toEqual({ runtimeProfile: 'review-profile', spec: {} });
    expect(promptRuntimeForDisplay({ spec: { model: 'operator-model', mode: 'api' } })).toEqual({ model: 'operator-model', mode: 'api' });
  });

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

  it("keeps a frontmatter profile pin out of explicit request overrides when the draft changes", () => {
    const pinned: PromptDetail = {
      ...detail("alpha", "alpha v1"),
      runtimeProfile: "Review",
      run: { ...EMPTY_RUN_REQUEST, runtimeProfile: "Review" },
    };

    const states = promptDetailReducer({}, { type: 'draft', detail: pinned, value: 'runtimeProfile: Plan' });
    expect(promptDetailStateFor(states, pinned).runRequest).toEqual(EMPTY_RUN_REQUEST);
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

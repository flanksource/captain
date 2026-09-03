import { describe, expect, it } from "vitest";
import { parseVerifyFrame, type VerifyReport } from "./verifyReport";

const BASE_REPORT: VerifyReport = {
  kind: "fixture",
  name: "acceptance",
  ran: true,
  passed: false,
  reason: "2 of 5 checks failed",
  iteration: 1,
  summary: {
    total: 5,
    passed: 3,
    failed: 2,
    warned: 0,
    skipped: 0,
    pending: 0,
    running: 0,
    timedout: 0,
  },
  tests: [
    {
      name: "renders the panel",
      framework: "vitest",
      passed: true,
      children: [],
    },
    {
      name: "handles the null report",
      framework: "vitest",
      failed: true,
      message: "expected null, got object",
    },
  ],
  checklist: [
    { item: "shows a status line", passed: true },
    { item: "never swallows an error", passed: null, message: "not judged yet" },
  ],
  state: "failed",
};

describe("parseVerifyFrame", () => {
  it("parses a running snapshot with a null report", () => {
    expect(parseVerifyFrame({ report: null, done: false })).toEqual({
      report: null,
      done: false,
    });
  });

  it("parses a full verdict report and preserves every field", () => {
    const frame = parseVerifyFrame({ report: BASE_REPORT, done: true });
    expect(frame).toEqual({ report: BASE_REPORT, done: true });
  });

  it("throws when the frame is not an object", () => {
    expect(() => parseVerifyFrame("not-a-frame")).toThrow(/expected an object/);
  });

  it('throws when "done" is missing or not a boolean', () => {
    expect(() => parseVerifyFrame({ report: null })).toThrow(/"done"/);
    expect(() => parseVerifyFrame({ report: null, done: "yes" })).toThrow(
      /"done"/,
    );
  });

  it("throws when the report's state is not a known VerifyState", () => {
    const drifted = { ...BASE_REPORT, state: "in_progress" };
    expect(() => parseVerifyFrame({ report: drifted, done: false })).toThrow(
      /state/,
    );
  });

  it("throws when a required report field has the wrong type", () => {
    const drifted = { ...BASE_REPORT, kind: 42 };
    expect(() => parseVerifyFrame({ report: drifted, done: false })).toThrow(
      /"kind"/,
    );
  });

  it("throws when the summary is missing a required counter", () => {
    const { timedout: _timedout, ...incompleteSummary } = BASE_REPORT.summary;
    const drifted = { ...BASE_REPORT, summary: incompleteSummary };
    expect(() => parseVerifyFrame({ report: drifted, done: false })).toThrow(
      /summary/,
    );
  });

  it("throws when a checklist item's passed field is not boolean-or-null", () => {
    const drifted = {
      ...BASE_REPORT,
      checklist: [{ item: "x", passed: "true" }],
    };
    expect(() => parseVerifyFrame({ report: drifted, done: false })).toThrow(
      /checklist/,
    );
  });

  it("throws when a test node is missing its name", () => {
    const drifted = { ...BASE_REPORT, tests: [{ passed: true }] };
    expect(() => parseVerifyFrame({ report: drifted, done: false })).toThrow(
      /name/,
    );
  });

  it("parses a test node's rolled-up summary field", () => {
    const nodeSummary = {
      total: 2,
      passed: 1,
      failed: 1,
      warned: 0,
      skipped: 0,
      pending: 0,
      running: 0,
      timedout: 0,
    };
    const drifted = {
      ...BASE_REPORT,
      tests: [{ name: "group", passed: false, summary: nodeSummary }],
    };
    const frame = parseVerifyFrame({ report: drifted, done: false });
    expect(frame.report?.tests?.[0]?.summary).toEqual(nodeSummary);
  });

  it("throws when a test node's summary has a wrong-typed counter", () => {
    const drifted = {
      ...BASE_REPORT,
      tests: [{ name: "group", summary: { ...BASE_REPORT.summary, total: "2" } }],
    };
    expect(() => parseVerifyFrame({ report: drifted, done: false })).toThrow(
      /"total"/,
    );
  });
});

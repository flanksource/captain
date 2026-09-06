import type {
  VerifyChecklistItem,
  VerifyNode,
  VerifyNodeContext,
  VerifyNodeProgress,
  VerifyReport,
  VerifyState,
  VerifySummary,
} from "@flanksource/clicky-ui/data";

export type { VerifyReport } from "@flanksource/clicky-ui/data";

/** Canonical order mirrors Go's `AllVerifyStates()`. */
export const VERIFY_STATES = [
  "queued",
  "running",
  "passed",
  "failed",
  "errored",
  "warned",
  "skipped",
  "cancelled",
  "timed_out",
] as const;

/** The `verify` SSE event payload: the newest report, and whether it is the
 * verdict (`done: true`) or a still-running snapshot (`done: false`). */
export interface VerifyFrame {
  report: VerifyReport | null;
  done: boolean;
}

/**
 * parseVerifyFrame validates an already-JSON-decoded value against the
 * VerifyFrame wire shape and throws a descriptive error on any drift — a
 * missing field, a wrong type, or a state string the Go side no longer emits.
 * There is no silent default: a malformed frame must fail loudly rather than
 * render a stale or blank verification panel.
 */
export function parseVerifyFrame(data: unknown): VerifyFrame {
  const frame = requireRecord(data, "verify frame");
  const done = requireBoolean(frame.done, 'verify frame: "done"');
  const report =
    frame.report === null || frame.report === undefined
      ? null
      : parseVerifyReport(frame.report);
  return { report, done };
}

function parseVerifyReport(value: unknown): VerifyReport {
  const r = requireRecord(value, "verify report");
  const state = requireString(r.state, 'verify report: "state"');
  if (!isVerifyState(state)) {
    throw new Error(
      `verify report: "state" must be one of ${VERIFY_STATES.join(", ")}, got ${JSON.stringify(state)}`,
    );
  }
  return {
    kind: requireString(r.kind, 'verify report: "kind"'),
    name: optionalString(r.name, 'verify report: "name"'),
    ran: requireBoolean(r.ran, 'verify report: "ran"'),
    passed: requireBoolean(r.passed, 'verify report: "passed"'),
    reason: optionalString(r.reason, 'verify report: "reason"'),
    feedback: optionalString(r.feedback, 'verify report: "feedback"'),
    iteration: requireNumber(r.iteration, 'verify report: "iteration"'),
    summary: parseVerifySummary(r.summary),
    tests: optionalArray(r.tests, 'verify report: "tests"', parseVerifyNode),
    checklist: optionalArray(
      r.checklist,
      'verify report: "checklist"',
      parseChecklistItem,
    ),
    state,
    started_at: optionalString(r.started_at, 'verify report: "started_at"'),
    finished_at: optionalString(r.finished_at, 'verify report: "finished_at"'),
    duration: optionalNumber(r.duration, 'verify report: "duration"'),
  };
}

function parseVerifySummary(value: unknown): VerifySummary {
  const s = requireRecord(value, "verify report summary");
  return {
    total: requireNumber(s.total, 'verify report summary: "total"'),
    passed: requireNumber(s.passed, 'verify report summary: "passed"'),
    failed: requireNumber(s.failed, 'verify report summary: "failed"'),
    warned: requireNumber(s.warned, 'verify report summary: "warned"'),
    skipped: requireNumber(s.skipped, 'verify report summary: "skipped"'),
    pending: requireNumber(s.pending, 'verify report summary: "pending"'),
    running: requireNumber(s.running, 'verify report summary: "running"'),
    timedout: requireNumber(s.timedout, 'verify report summary: "timedout"'),
  };
}

function parseVerifyNode(value: unknown): VerifyNode {
  const n = requireRecord(value, "verify test node");
  return {
    name: requireString(n.name, 'verify test node: "name"'),
    framework: optionalString(n.framework, 'verify test node: "framework"'),
    task_id: optionalString(n.task_id, 'verify test node: "task_id"'),
    file: optionalString(n.file, 'verify test node: "file"'),
    line: optionalNumber(n.line, 'verify test node: "line"'),
    message: optionalString(n.message, 'verify test node: "message"'),
    command: optionalString(n.command, 'verify test node: "command"'),
    work_dir: optionalString(n.work_dir, 'verify test node: "work_dir"'),
    stdout: optionalString(n.stdout, 'verify test node: "stdout"'),
    stderr: optionalString(n.stderr, 'verify test node: "stderr"'),
    duration: optionalNumber(n.duration, 'verify test node: "duration"'),
    passed: optionalBoolean(n.passed, 'verify test node: "passed"'),
    failed: optionalBoolean(n.failed, 'verify test node: "failed"'),
    warned: optionalBoolean(n.warned, 'verify test node: "warned"'),
    skipped: optionalBoolean(n.skipped, 'verify test node: "skipped"'),
    pending: optionalBoolean(n.pending, 'verify test node: "pending"'),
    running: optionalBoolean(n.running, 'verify test node: "running"'),
    timed_out: optionalBoolean(n.timed_out, 'verify test node: "timed_out"'),
    progress: n.progress === undefined ? undefined : parseProgress(n.progress),
    context: n.context === undefined ? undefined : parseContext(n.context),
    summary: n.summary === undefined ? undefined : parseVerifySummary(n.summary),
    detail: n.detail,
    children: optionalArray(
      n.children,
      'verify test node: "children"',
      parseVerifyNode,
    ),
  };
}

function parseProgress(value: unknown): VerifyNodeProgress {
  const p = requireRecord(value, "verify test node progress");
  return {
    phase: optionalString(p.phase, 'verify test node progress: "phase"'),
    done: requireNumber(p.done, 'verify test node progress: "done"'),
    total: requireNumber(p.total, 'verify test node progress: "total"'),
  };
}

function parseContext(value: unknown): VerifyNodeContext {
  const c = requireRecord(value, "verify test node context");
  return {
    command: optionalString(c.command, 'verify test node context: "command"'),
    exit_code: requireNumber(
      c.exit_code,
      'verify test node context: "exit_code"',
    ),
    cwd: optionalString(c.cwd, 'verify test node context: "cwd"'),
    cel_expression: optionalString(
      c.cel_expression,
      'verify test node context: "cel_expression"',
    ),
    cel_vars:
      c.cel_vars === undefined
        ? undefined
        : requireRecord(c.cel_vars, 'verify test node context: "cel_vars"'),
    expected: c.expected,
    actual: c.actual,
  };
}

function parseChecklistItem(value: unknown): VerifyChecklistItem {
  const c = requireRecord(value, "verify report checklist item");
  const passed = c.passed;
  if (passed !== null && typeof passed !== "boolean") {
    throw new Error(
      `verify report checklist item: "passed" must be a boolean or null, got ${typeOf(passed)}`,
    );
  }
  return {
    item: requireString(c.item, 'verify report checklist item: "item"'),
    passed,
    message: optionalString(
      c.message,
      'verify report checklist item: "message"',
    ),
  };
}

function isVerifyState(value: string): value is VerifyState {
  return (VERIFY_STATES as readonly string[]).includes(value);
}

function typeOf(value: unknown): string {
  if (value === null) return "null";
  if (Array.isArray(value)) return "array";
  return typeof value;
}

function requireRecord(
  value: unknown,
  label: string,
): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error(`${label}: expected an object, got ${typeOf(value)}`);
  }
  return value as Record<string, unknown>;
}

function requireString(value: unknown, label: string): string {
  if (typeof value !== "string") {
    throw new Error(`${label} must be a string, got ${typeOf(value)}`);
  }
  return value;
}

function optionalString(value: unknown, label: string): string | undefined {
  if (value === undefined) return undefined;
  return requireString(value, label);
}

function requireNumber(value: unknown, label: string): number {
  if (typeof value !== "number") {
    throw new Error(`${label} must be a number, got ${typeOf(value)}`);
  }
  return value;
}

function optionalNumber(value: unknown, label: string): number | undefined {
  if (value === undefined) return undefined;
  return requireNumber(value, label);
}

function requireBoolean(value: unknown, label: string): boolean {
  if (typeof value !== "boolean") {
    throw new Error(`${label} must be a boolean, got ${typeOf(value)}`);
  }
  return value;
}

function optionalBoolean(value: unknown, label: string): boolean | undefined {
  if (value === undefined) return undefined;
  return requireBoolean(value, label);
}

function optionalArray<T>(
  value: unknown,
  label: string,
  parseItem: (item: unknown) => T,
): T[] | undefined {
  if (value === undefined) return undefined;
  if (!Array.isArray(value)) {
    throw new Error(`${label} must be an array, got ${typeOf(value)}`);
  }
  return value.map(parseItem);
}

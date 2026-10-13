// Run with `node --test bash_guard.test.ts` (driven by git_commit_guard_ginkgo_test.go).
import assert from "node:assert/strict";
import { test } from "node:test";
import { preToolUseGuards } from "./bash_guard.ts";

const bash = (command: string) => ({
  hook_event_name: "PreToolUse",
  tool_name: "Bash",
  tool_input: { command },
});

async function decide(guard: boolean, command: string) {
  const outputs = [];
  for (const matcher of preToolUseGuards(guard)) {
    for (const hook of matcher.hooks) {
      outputs.push(await hook(bash(command)));
    }
  }
  return outputs;
}

test("a guarded run blocks git add and git commit", async () => {
  for (const command of ["git add main.go", "cd repo && git commit -m fix"]) {
    const [output] = await decide(true, command);
    assert.equal(output.decision, "block", command);
  }
});

test("an unguarded run registers no Bash guard, so git add reaches the permission rules", async () => {
  assert.deepEqual(preToolUseGuards(false), []);
  assert.deepEqual(await decide(false, "git add main.go"), []);
});

test("no other Bash command is ever approved by the guard", async () => {
  for (const command of ["git push origin main", "ls -la", "git rebase --continue"]) {
    for (const output of await decide(true, command)) {
      assert.deepEqual(output, {}, command);
    }
  }
});

// The PreToolUse guards the bridge installs on Bash. Kept free of runtime
// imports so `node --test bash_guard.test.ts` exercises it without the SDK.
//
// A guard either blocks or returns {} — never `decision: "approve"`. The SDK
// maps approve to a hook "allow", which skips the permission prompt (canUseTool,
// captain's approval broker) for every command no rule matched; returning no
// decision leaves allow/ask/deny rules and the broker in charge.

const gitCommitPattern = /\bgit\s+(add|commit)\b/;

type GuardOutput = { decision?: "block"; reason?: string };
type GuardMatcher = {
  matcher: string;
  hooks: Array<(input: unknown) => Promise<GuardOutput>>;
};

async function gitCommitGuard(input: unknown): Promise<GuardOutput> {
  const command =
    (input as { tool_input?: { command?: string } }).tool_input?.command || "";
  if (gitCommitPattern.test(command)) {
    return {
      decision: "block",
      reason: "git add/commit is managed by captain, not the agent",
    };
  }
  return {};
}

// preToolUseGuards is the Bash guard list for a run: the git add/commit guard
// only when captain owns the run's commits (initialize's guardGitCommits), and
// nothing otherwise.
export function preToolUseGuards(guardGitCommits: boolean): GuardMatcher[] {
  return guardGitCommits ? [{ matcher: "Bash", hooks: [gitCommitGuard] }] : [];
}

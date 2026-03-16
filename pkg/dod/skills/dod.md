---
name: dod
description: "Set Definition of Done commands that must pass before Claude can stop. Usage: /dod make test lint"
allowed-tools: [Bash]
---

# Definition of Done (DoD)

You are setting up a Definition of Done gate. The user has provided commands that must pass before you (Claude) can stop working.

## What to do

The user typed `/dod $ARGUMENTS`. Parse the arguments as shell command(s) and register them using `captain dod set`.

**Steps:**

1. Extract the session ID from the environment. The session ID is available from the transcript path or can be derived. Use the hook input's `session_id` field if available, otherwise find it from `~/.claude/projects/` session files for the current directory.

2. Run the following command to register the DoD:

```bash
captain dod set --session-id "<SESSION_ID>" --workdir "$(pwd)" $ARGUMENTS
```

Where `$ARGUMENTS` are the commands the user provided (e.g., `make test lint`).

3. Confirm to the user what DoD has been set.

**Important:**
- If `$ARGUMENTS` is empty, run `captain dod status --session-id "<SESSION_ID>"` instead to show current DoD.
- The session ID should be extracted from the `CLAUDE_SESSION_ID` environment variable, or from the transcript path, or by listing recent session files.

## Example

User types: `/dod make test lint`

You run:
```bash
captain dod set --session-id "abc-123" --workdir "/path/to/project" "make test lint"
```

Then confirm: "DoD set: `make test lint` must pass before I can stop."

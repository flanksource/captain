---
name: dod-run
description: "Manually run DoD checks to see current pass/fail state"
allowed-tools: [Bash]
---

# Run DoD Checks

Manually run all DoD commands to see their current status without triggering a stop.

## What to do

Run:
```bash
captain dod run --session-id "<SESSION_ID>"
```

Display the results showing pass/fail for each command with output.

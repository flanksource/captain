# GitHub security updates

Use [security-updates.prompt](security-updates.prompt) to run a separate Captain session for each repository in a GitHub organization that has an exact topic. The GitHub token determines which repositories the fan-out can see. Captain runs up to three repository sessions at a time.

## Prerequisites

Install a Captain build that supports `fanout.exec`, Python 3, and Gavel. Apply and publish modes also need Git, Worktrunk (`wt`), Repomap with `deps update --security`, and the package managers and test tools used by the repositories.

Configure GitHub credentials on the Captain host. The fan-out uses `GITHUB_TOKEN` or `GH_TOKEN` if Captain passes it to the shell, or the credential saved by Gavel in `~/.config/gavel/auth.json`. Give the token access to the repositories and alert endpoints you want to audit. Configure `GITHUB_TOKEN` or `GH_TOKEN` in the per-repository session environment for Repomap, plus credentials for Git fetches in write modes. Publish mode needs permission to push branches, open PRs, and enable auto-merge in repositories that allow it. Keep tokens out of prompt inputs.

## Run

From the Captain repository root, start with an audit. Replace `example-org` and `platform` with your organization and topic.

```sh
captain prompt run examples/security-updates.prompt \
  --var org=example-org --var topic=platform --json
```

In the prompt workbench, open **Run**, enter **org** and **topic**, and choose an **action**.

| Action | Result |
| --- | --- |
| `audit` | Reports Dependabot alerts, CodeQL findings, and PR health from Gavel. |
| `apply` | Applies and verifies supported Dependabot fixes in an isolated Worktrunk worktree, then returns its path. |
| `publish` | Applies and verifies eligible fixes, then opens PRs with `gavel commit -p --auto-merge`. With `rebase: true`, also rebases eligible security PRs owned by the authenticated user. |

To publish fixes with auto-merge and rebase eligible PRs:

```sh
captain prompt run examples/security-updates.prompt \
  --var org=example-org --var topic=platform \
  --var action=publish --var rebase:=true --json
```

Each session reports its audit coverage, verification results, PR URLs or worktree paths, and any findings that require a maintainer. Archived, disabled, and forked repositories receive an audit only. GitHub auto-merge waits for the repository's required checks and reviews.

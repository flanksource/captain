package gitagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/api"
)

// Control payload file names (§4).
const (
	ControlTaskFile    = "task.json"
	ControlHooksFile   = "hooks.json"
	ControlPolicyFile  = "policy.json"
	ControlVerdictFile = "verdict.json"
)

// HookSets is the hooks.json wire payload. The supervisor chooses both tiers;
// receivers persist this value with task state so retries vet the same policy.
type HookSets struct {
	Sidecar    *api.Workflow `json:"sidecar,omitempty"`
	Supervisor *api.Workflow `json:"supervisor,omitempty"`
}

// DecodeHookSets validates a hooks.json payload before it becomes durable task
// policy. Unknown fields fail closed instead of silently disabling a tier.
func DecodeHookSets(data []byte) (*HookSets, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}")
	}
	var hooks HookSets
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&hooks); err != nil {
		return nil, fmt.Errorf("decode hooks.json: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return nil, fmt.Errorf("decode hooks.json: %w", err)
	}
	for tier, workflow := range map[string]*api.Workflow{
		"sidecar": hooks.Sidecar, "supervisor": hooks.Supervisor,
	} {
		if err := workflow.Validate(); err != nil {
			return nil, fmt.Errorf("hooks.%s: %w", tier, err)
		}
	}
	return &hooks, nil
}

// BuildControlCommit writes payloads as a flat tree and wraps it in a
// parentless commit. Control refs point at commits, never bare trees — a
// tree-tipped ref trips gc, bitmap and fsck paths (R3.3). env matters: built
// with a hook environment the objects land in quarantine and are discarded
// with a rejected push; nil means the process environment, scrubbed.
func BuildControlCommit(ctx context.Context, repoDir string, env []string, payloads map[string][]byte) (string, error) {
	if len(payloads) == 0 {
		return "", fmt.Errorf("a control commit needs at least one payload")
	}
	if env == nil {
		env = ScrubGitEnv(os.Environ())
	}
	names := make([]string, 0, len(payloads))
	for name := range payloads {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
			return "", fmt.Errorf("control payload name %q must be a bare file name", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var tree strings.Builder
	for _, name := range names {
		oid, err := runGitIn(ctx, repoDir, env, strings.NewReader(string(payloads[name])),
			"hash-object", "-w", "--no-filters", "--stdin")
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&tree, "100644 blob %s\t%s\n", oid, name)
	}
	treeOID, err := runGitIn(ctx, repoDir, env, strings.NewReader(tree.String()), "mktree")
	if err != nil {
		return "", err
	}
	cenv := envWith(env,
		"GIT_AUTHOR_NAME=captain",
		"GIT_AUTHOR_EMAIL=captain@localhost",
		"GIT_COMMITTER_NAME=captain",
		"GIT_COMMITTER_EMAIL=captain@localhost",
	)
	return runGitIn(ctx, repoDir, cenv,
		strings.NewReader("captain control envelope payloads\n"),
		"commit-tree", treeOID)
}

// ReadControlPayload reads one payload file from a control commit's tree,
// readable in pre-receive through the quarantine object directories.
func ReadControlPayload(ctx context.Context, repoDir string, env []string, controlCommit, name string) ([]byte, error) {
	out, err := runGitRaw(ctx, repoDir, env, nil, "cat-file", "blob", controlCommit+":"+name)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

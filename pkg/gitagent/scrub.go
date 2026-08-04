package gitagent

import "strings"

// receivePackEnv are the variables receive-pack injects that redirect any
// descendant git at another repository. GIT_QUARANTINE_PATH is listed by name
// because `git rev-parse --local-env-vars` omits it (verified, §1.1), so the
// githooks(5) scrub idiom leaves it set — the exact trap R1.1 exists for.
var receivePackEnv = []string{
	"GIT_QUARANTINE_PATH",
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_COMMON_DIR",
}

// ScrubGitEnv returns env without any receive-pack repository redirection and
// without the push-option variables, for hook subprocesses (R1.1).
func ScrubGitEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if scrubbedGitVar(name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func scrubbedGitVar(name string) bool {
	if strings.HasPrefix(name, "GIT_PUSH_OPTION_") {
		return true
	}
	for _, v := range receivePackEnv {
		if name == v {
			return true
		}
	}
	return false
}

// RelayEnv returns env with only GIT_QUARANTINE_PATH removed. The relay push
// from inside pre-receive must keep the inherited object directories so the
// quarantined objects stay readable, and must not copy them (R1.4, verified
// §1.4).
func RelayEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); name == "GIT_QUARANTINE_PATH" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// envWith returns env with the given KEY=VALUE pairs appended, each replacing
// any existing entry for its key.
func envWith(env []string, pairs ...string) []string {
	out := make([]string, 0, len(env)+len(pairs))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		replaced := false
		for _, p := range pairs {
			if pname, _, _ := strings.Cut(p, "="); pname == name {
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, kv)
		}
	}
	return append(out, pairs...)
}

package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// JoinMountPath is where the token file appears inside the workload.
const JoinMountPath = "/run/captain/join"

// CredentialsMountPath is where the redacted agent logins appear inside the
// workload, on both targets. A directory rather than a file, because the
// sidecar reads whichever providers the supervisor happens to publish and
// because a Kubernetes Secret mounted at a file path stops receiving updates.
const CredentialsMountPath = "/run/captain/credentials"

// dockerBinary is the client this package drives. Captain shells out to the
// docker CLI everywhere rather than linking the SDK, and this follows suit: the
// CLI resolves DOCKER_HOST, contexts and credential helpers on its own.
const dockerBinary = "docker"

// DockerArgs builds the `docker run` argv for a sidecar.
//
// It is pure so the security posture can be asserted in a unit test — the flags
// below are the entire containment boundary for agent-authored code, so a
// silent regression in any of them is the failure this package exists to
// prevent. joinHostPath is the host-side token file, bind-mounted read-only.
func DockerArgs(plan Plan, joinHostPath string) []string {
	args := []string{
		"run", "--detach",
		"--name", plan.WorkloadName(),
		"--restart", "unless-stopped",
		// Reap orphans: the coding agent is launched with Setsid, so its children
		// reparent to PID 1, and captain is not a reaper. Without this, zombies
		// accumulate against --pids-limit until forks start failing.
		"--init",
	}
	for _, label := range sortedLabelArgs(plan.Labels()) {
		args = append(args, "--label", label)
	}

	// Override the image entrypoint rather than run through it. The published
	// image ends `USER root` and its entrypoint calls gosu to drop privileges,
	// which needs CAP_SETUID/CAP_SETGID — exactly what --cap-drop ALL removes.
	// Invoking the binary directly and letting docker set the uid means the
	// process never runs as root at all, which beats dropping from it.
	args = append(args,
		"--entrypoint", "captain",
		"--user", fmt.Sprintf("%d:%d", plan.Security.RunAsUser, plan.Security.RunAsGroup),
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
	)
	for _, capability := range plan.Security.CapAdd {
		args = append(args, "--cap-add", capability)
	}
	if plan.Security.ReadOnlyRoot {
		// Safe only because every write target is relocated: state onto the
		// volume under HOME, scratch onto this tmpfs.
		args = append(args, "--read-only",
			"--tmpfs", fmt.Sprintf("/tmp:rw,nosuid,nodev,mode=1777,size=%s", plan.Sizing.DockerTmpfsSize()))
	}

	args = append(args,
		"--network", plan.Security.Network,
		// Resolves to the bridge gateway on Linux and is built in on Docker
		// Desktop, so the sidecar reaches the mailbox the same way on both.
		"--add-host", "host.docker.internal:host-gateway",
		// Loopback-published: the supervisor dispatches from this host, and the
		// sidecar has no reason to be reachable from the LAN.
		"--publish", fmt.Sprintf("127.0.0.1:%d:%d", plan.HostPort, plan.ListenPort),
	)

	args = append(args,
		"--cpus", plan.Sizing.DockerCPUs(),
		"--memory", plan.Sizing.DockerMemoryBytes(),
		// Equal to --memory: swapping lets a runaway agent exceed its ceiling by
		// paging instead of failing.
		"--memory-swap", plan.Sizing.DockerMemoryBytes(),
		"--memory-reservation", plan.Sizing.DockerMemoryReservationBytes(),
	)
	if plan.Sizing.PidsLimit > 0 {
		args = append(args, "--pids-limit", fmt.Sprintf("%d", plan.Sizing.PidsLimit))
	}

	args = append(args,
		"--volume", plan.VolumeName()+":"+plan.Home,
		// A path, not a credential: passing HOME by name would clobber the docker
		// CLIENT's own HOME and break registry auth on the pull.
		"--env", "HOME="+plan.Home,
		// Keep Go and npm caches off the RAM-backed tmpfs, which counts against
		// the memory limit.
		"--env", "TMPDIR="+plan.Home+"/.cache/tmp",
	)
	if joinHostPath != "" {
		args = append(args, "--volume", joinHostPath+":"+plan.JoinPath+":ro")
	}
	if plan.CredentialsDir != "" {
		// Read-only: the sidecar copies out of here, and the supervisor is the
		// only writer.
		args = append(args, "--volume", plan.CredentialsDir+":"+CredentialsMountPath+":ro")
	}
	// Names only. Docker resolves each from the client environment, so a
	// credential value never enters argv or `docker inspect`.
	for _, name := range plan.EnvNames {
		args = append(args, "--env", name)
	}

	args = append(args, plan.Image)
	return append(args, plan.ServeArgs()...)
}

// sortedLabelArgs renders labels as key=value in a stable order, so a rendered
// argv can be compared against a golden value.
func sortedLabelArgs(labels map[string]string) []string {
	rendered := make([]string, 0, len(labels))
	for _, key := range sortedKeys(labels) {
		rendered = append(rendered, key+"="+labels[key])
	}
	return rendered
}

// DockerAvailable reports whether a usable daemon is reachable, so deploy fails
// before minting a token rather than after.
func DockerAvailable(ctx context.Context) error {
	if _, err := exec.LookPath(dockerBinary); err != nil {
		return fmt.Errorf("docker is not on PATH: %w", err)
	}
	if out, err := exec.CommandContext(ctx, dockerBinary, "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		return fmt.Errorf("docker daemon is not reachable: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// DockerImagePresent reports whether the image is already local, matching the
// exit-code probe pkg/container uses.
func DockerImagePresent(ctx context.Context, image string) bool {
	return exec.CommandContext(ctx, dockerBinary, "image", "inspect", "--format", "{{.Id}}", image).Run() == nil
}

// DockerPull fetches the image before a token is minted.
//
// This image carries a Go toolchain, Chromium and several agent CLIs, so a cold
// pull is slow and is where a deploy is most likely to fail. Pulling before the
// mint keeps a failure from leaving behind a live credential for a workload
// that never started.
func DockerPull(ctx context.Context, image string) error {
	cmd := exec.CommandContext(ctx, dockerBinary, "pull", image)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr // progress is not the result
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	return nil
}

// DockerContainer is the state of an existing sidecar container.
type DockerContainer struct {
	ID      string
	Running bool
	Image   string
}

// DockerInspect reports an existing container, or ok=false when there is none.
func DockerInspect(ctx context.Context, name string) (DockerContainer, bool, error) {
	out, err := exec.CommandContext(ctx, dockerBinary, "container", "inspect",
		"--format", "{{.Id}} {{.State.Running}} {{.Config.Image}}", name).Output()
	if err != nil {
		// `inspect` exits non-zero for "no such container", which is the common
		// case here rather than a failure.
		return DockerContainer{}, false, nil
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) < 3 {
		return DockerContainer{}, false, fmt.Errorf("unexpected docker inspect output for %s: %q", name, out)
	}
	return DockerContainer{ID: fields[0], Running: fields[1] == "true", Image: fields[2]}, true, nil
}

// DockerRun starts the sidecar and returns its container id.
func DockerRun(ctx context.Context, plan Plan, joinHostPath string) (string, error) {
	out, err := exec.CommandContext(ctx, dockerBinary, DockerArgs(plan, joinHostPath)...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker run: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// DockerStart restarts an existing, stopped sidecar.
func DockerStart(ctx context.Context, name string) error {
	if out, err := exec.CommandContext(ctx, dockerBinary, "start", name).CombinedOutput(); err != nil {
		return fmt.Errorf("docker start %s: %s", name, strings.TrimSpace(string(out)))
	}
	return nil
}

// DockerRemove deletes the container, and its state volume only when asked.
//
// The volume holds the agent's private key, so removing it is what makes an
// identity unrecoverable rather than merely stopped — it stays opt-in.
func DockerRemove(ctx context.Context, plan Plan, purgeVolume bool) error {
	if out, err := exec.CommandContext(ctx, dockerBinary, "rm", "--force", plan.WorkloadName()).CombinedOutput(); err != nil {
		if !strings.Contains(string(out), "No such container") {
			return fmt.Errorf("docker rm %s: %s", plan.WorkloadName(), strings.TrimSpace(string(out)))
		}
	}
	if !purgeVolume {
		return nil
	}
	if out, err := exec.CommandContext(ctx, dockerBinary, "volume", "rm", plan.VolumeName()).CombinedOutput(); err != nil {
		if !strings.Contains(string(out), "no such volume") {
			return fmt.Errorf("docker volume rm %s: %s", plan.VolumeName(), strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// DockerLogs returns the workload's recent output, so a timeout reports why
// rather than only that it happened.
func DockerLogs(ctx context.Context, name string, lines int) string {
	out, err := exec.CommandContext(ctx, dockerBinary, "logs", "--tail", fmt.Sprintf("%d", lines), name).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

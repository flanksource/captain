package deploy

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/sandbox"
)

// Security is the confinement applied to the sidecar.
//
// The posture is fixed rather than a menu, because the workload's requirements
// pin almost all of it. What is adjustable is adjustable for a stated reason:
// ReadOnlyRoot for images that write outside $HOME, and CapAdd for runtimes
// that need a capability back. There is deliberately no privileged mode and no
// mount passthrough — see RefuseUnsafe.
type Security struct {
	// RunAsUser/RunAsGroup must own Home in the image.
	RunAsUser  int
	RunAsGroup int

	// ReadOnlyRoot mounts the image root read-only. This holds only because
	// every write target is relocated: config, keys and repos live on the state
	// volume under Home, and scratch goes to a writable /tmp with TMPDIR steered
	// onto the volume so Go and npm caches do not exhaust it.
	ReadOnlyRoot bool

	// CapAdd are capabilities restored on top of an otherwise empty set.
	//
	// The set is empty by default, which is only possible because the workload
	// overrides the image entrypoint. The published image ends `USER root` with
	// an entrypoint that calls gosu to drop privileges, and gosu needs
	// CAP_SETUID/CAP_SETGID — exactly what dropping everything removes. Invoking
	// the binary directly and letting the runtime set the uid means the process
	// never runs as root at all, which is strictly better than dropping from it.
	CapAdd []string

	// Network is the docker network. Ignored for Kubernetes.
	Network string
}

// HardenedSecurity is the default posture.
func HardenedSecurity() Security {
	return Security{
		RunAsUser:    501, // the image's `claude` user (pkg/container/base/Dockerfile)
		RunAsGroup:   20,
		ReadOnlyRoot: true,
		Network:      "bridge",
	}
}

// Describe renders the posture for the command's result and for --dry-run.
func (s Security) Describe() string {
	parts := []string{
		fmt.Sprintf("uid=%d:%d", s.RunAsUser, s.RunAsGroup),
		"caps=none",
		"no-new-privileges",
		"seccomp=default",
	}
	if len(s.CapAdd) > 0 {
		parts[1] = "caps=+" + strings.Join(s.CapAdd, ",")
	}
	if s.ReadOnlyRoot {
		parts = append(parts, "read-only-root")
	}
	return strings.Join(parts, " ")
}

// unsafeNetworks are docker network modes that dissolve the boundary the
// container is supposed to be. `host` puts the workload on the host's network
// namespace, where the mailbox's own loopback listener becomes reachable;
// `none` removes the inbound dispatch path and the outbound relay, so the
// sidecar cannot work at all.
var unsafeNetworks = map[string]string{
	"host": "shares the host network namespace, exposing loopback-bound services to agent-authored code",
	"none": "removes the dispatch, relay and model-API paths the sidecar needs to function",
}

// RefuseUnsafe rejects a configuration that would make the container boundary
// decorative. It refuses rather than filters, following the precedent set for
// untrusted container config: silently granting less than was asked for still
// grants something the operator never reviewed.
//
// home is the sandboxed user's home inside the workload, for the rootless
// Docker socket path.
func RefuseUnsafe(security Security, home string, mounts []string, presets []string) error {
	if security.RunAsUser == 0 {
		return fmt.Errorf("--run-as-user 0 runs agent-authored code as root inside the workload; pick the image's unprivileged uid")
	}
	if reason, unsafe := unsafeNetworks[strings.ToLower(strings.TrimSpace(security.Network))]; unsafe {
		return fmt.Errorf("--network %s %s", security.Network, reason)
	}
	if err := refuseRuntimeSockets(home, mounts); err != nil {
		return err
	}
	return refuseSocketPresets(presets)
}

// refuseRuntimeSockets blocks any mount reaching a container-runtime endpoint.
// A process that can talk to the daemon can start a privileged container
// bind-mounting the host root, so this is a full escape and R5.3 makes it
// non-waivable. Comparison is on the cleaned source path, and the deny list is
// shared with the SRT adapter so the two cannot drift.
func refuseRuntimeSockets(home string, mounts []string) error {
	denied := map[string]struct{}{}
	for _, socket := range sandbox.ContainerRuntimeSockets(home) {
		denied[filepath.Clean(socket)] = struct{}{}
	}
	for _, mount := range mounts {
		source := filepath.Clean(strings.TrimSpace(strings.SplitN(mount, ":", 2)[0]))
		if _, blocked := denied[source]; blocked {
			return fmt.Errorf(
				"refusing to mount the container runtime socket %s into a git-agent sidecar: "+
					"it is a full host escape and makes every other control here decorative (R5.3, A6.2)", source)
		}
	}
	return nil
}

// socketPresets expand into a real socket bind mount, so selecting one is the
// same escape by another name. `claude` additionally sets
// enableWeakerNetworkIsolation, which R5.3 forbids in the same sentence.
var socketPresets = []string{"claude", "docker"}

func refuseSocketPresets(presets []string) error {
	selected := map[string]struct{}{}
	for _, preset := range presets {
		selected[strings.ToLower(strings.TrimSpace(preset))] = struct{}{}
	}
	var found []string
	for _, preset := range socketPresets {
		if _, ok := selected[preset]; ok {
			found = append(found, preset)
		}
	}
	if len(found) == 0 {
		return nil
	}
	sort.Strings(found)
	return fmt.Errorf(
		"sandbox preset %s grants the container runtime socket, which a git-agent sidecar must never hold (R5.3, A6.2); "+
			"remove it from the backend before deploying", strings.Join(found, " and "))
}

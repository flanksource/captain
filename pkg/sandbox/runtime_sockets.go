package sandbox

import "path/filepath"

// ContainerRuntimeSockets lists the container-runtime endpoints that must never
// be reachable from a sandbox.
//
// Write access to any of these is a full host escape: a process that can talk
// to the daemon can start a privileged container bind-mounting `/`, which makes
// every other confinement in the system decorative. SPEC-git-agent-protocol
// R5.3 states this is "not waivable by configuration".
//
// It lives here, rather than beside either caller, because two unrelated
// subsystems have to agree on it: the SRT adapter denies reads of these paths,
// and git-agent deployment refuses to mount them. A second copy would drift,
// and the failure mode of drift is a silent escape hatch.
//
// home is the sandboxed user's home directory; rootless Docker keeps its socket
// under it.
func ContainerRuntimeSockets(home string) []string {
	return []string{
		filepath.Join(home, ".docker", "run", "docker.sock"),
		"/var/run/docker.sock",
		"/run/docker.sock",
		"/run/containerd/containerd.sock",
		"/run/podman/podman.sock",
	}
}

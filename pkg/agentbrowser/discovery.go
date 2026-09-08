package agentbrowser

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// dashboardSidecarName is agent-browser's dashboard server, which writes a
// dashboard.pid next to the session sidecars but is not a session.
const dashboardSidecarName = "dashboard"

// browserSidecar is one agent-browser session as it exists on disk. A session
// has no id and no metadata of its own — it is a name plus this set of files.
type browserSidecar struct {
	Session    string `json:"session"`
	Namespace  string `json:"namespace,omitempty"`
	Dir        string `json:"dir"`
	DaemonPID  int    `json:"daemonPid,omitempty"`
	SocketPath string `json:"socketPath,omitempty"`
	Version    string `json:"version,omitempty"`
	Engine     string `json:"engine,omitempty"`
	Provider   string `json:"provider,omitempty"`
	ConfigHash string `json:"configHash,omitempty"`
	StreamPort int    `json:"streamPort,omitempty"`
}

// agentBrowserSocketDir resolves the directory agent-browser keeps its daemon
// sidecars in, in the same precedence order agent-browser itself uses.
func agentBrowserSocketDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("AGENT_BROWSER_SOCKET_DIR")); dir != "" {
		return dir, nil
	}
	if runtimeDir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); runtimeDir != "" {
		return filepath.Join(runtimeDir, "agent-browser"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agent-browser"), nil
}

// scanBrowserSidecars enumerates every agent-browser session under a socket
// directory, including the ones relocated into namespace roots.
//
// Sessions are keyed on the .pid and .sock sidecars only. Keying on .config
// would be wrong: agent-browser never prunes those, and a working machine
// accumulates over a thousand of them for sessions that ended long ago.
func scanBrowserSidecars(socketDir string) ([]browserSidecar, error) {
	var sidecars []browserSidecar
	for _, root := range browserRoots(socketDir) {
		found, err := scanBrowserRoot(root)
		if err != nil {
			return nil, err
		}
		sidecars = append(sidecars, found...)
	}
	return sidecars, nil
}

// browserRoot is one directory of daemon sidecars: the socket dir itself, or a
// namespace's run directory.
type browserRoot struct {
	Namespace string
	Dir       string
}

func browserRoots(socketDir string) []browserRoot {
	roots := []browserRoot{{Dir: socketDir}}
	namespaces, err := os.ReadDir(filepath.Join(socketDir, "namespaces"))
	if err != nil {
		return roots
	}
	for _, entry := range namespaces {
		if !entry.IsDir() {
			continue
		}
		roots = append(roots, browserRoot{
			Namespace: entry.Name(),
			Dir:       filepath.Join(socketDir, "namespaces", entry.Name(), "run"),
		})
	}
	return roots
}

func scanBrowserRoot(root browserRoot) ([]browserSidecar, error) {
	entries, err := os.ReadDir(root.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sessions := make(map[string]bool)
	for _, entry := range entries {
		name, extension := splitSidecarName(entry.Name())
		if name == "" || name == dashboardSidecarName {
			continue
		}
		if extension == "pid" || extension == "sock" {
			sessions[name] = true
		}
	}
	sidecars := make([]browserSidecar, 0, len(sessions))
	for _, name := range sortedNames(sessions) {
		sidecars = append(sidecars, readBrowserSidecar(root, name))
	}
	return sidecars, nil
}

func readBrowserSidecar(root browserRoot, session string) browserSidecar {
	sidecar := browserSidecar{
		Session:    session,
		Namespace:  root.Namespace,
		Dir:        root.Dir,
		DaemonPID:  readSidecarInt(root.Dir, session, "pid"),
		Version:    readSidecarString(root.Dir, session, "version"),
		Engine:     readSidecarString(root.Dir, session, "engine"),
		Provider:   readSidecarString(root.Dir, session, "provider"),
		ConfigHash: readSidecarString(root.Dir, session, "config"),
		StreamPort: readSidecarInt(root.Dir, session, "stream"),
	}
	socketPath := filepath.Join(root.Dir, session+".sock")
	if _, err := os.Stat(socketPath); err == nil {
		sidecar.SocketPath = socketPath
	}
	return sidecar
}

func readSidecarString(dir, session, extension string) string {
	content, err := os.ReadFile(filepath.Join(dir, session+"."+extension))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(content))
}

func readSidecarInt(dir, session, extension string) int {
	value, err := strconv.Atoi(readSidecarString(dir, session, extension))
	if err != nil {
		return 0
	}
	return value
}

// splitSidecarName splits "<session>.<extension>". Session names are limited to
// alphanumerics, hyphens and underscores, so the final dot is the separator.
func splitSidecarName(fileName string) (string, string) {
	index := strings.LastIndex(fileName, ".")
	if index <= 0 {
		return "", ""
	}
	return fileName[:index], fileName[index+1:]
}

func sortedNames(set map[string]bool) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

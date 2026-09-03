package cli

import (
	"sync"

	"github.com/flanksource/captain/pkg/captainconfig"
)

// captainConfigResult is one memoized parse of ~/.captain.yaml, together with
// the path it was read from. The path is what keeps the cache honest:
// captainconfig.SetPath redirects the file (a git receive hook runs under the
// pusher's HOME, and every spec redirects it to a temp dir), and a bare
// sync.Once would hand the second caller the first caller's file.
type captainConfigResult struct {
	loaded bool
	path   string
	config captainconfig.Config
	exists bool
	err    error
}

var (
	captainConfigMu    sync.Mutex
	captainConfigCache captainConfigResult
)

// LoadCaptainConfigOnce reads ~/.captain.yaml at most once per process and path,
// returning the same result — success or failure — to every later caller.
//
// The root command installs several things out of that one file before any
// subcommand runs (the opt-out set, the fixture verifier); each doing its own
// Load meant re-reading and re-parsing the same YAML on every invocation, and
// left open the window where two installers disagree because the file changed
// between them. The bool is captainconfig.Load's: false means the file does not
// exist, which is a normal first-run state rather than an error.
func LoadCaptainConfigOnce() (captainconfig.Config, bool, error) {
	path, err := captainconfig.Path()
	if err != nil {
		return captainconfig.Config{}, false, err
	}
	captainConfigMu.Lock()
	defer captainConfigMu.Unlock()
	if captainConfigCache.loaded && captainConfigCache.path == path {
		return captainConfigCache.config, captainConfigCache.exists, captainConfigCache.err
	}
	config, exists, loadErr := captainconfig.Load()
	captainConfigCache = captainConfigResult{
		loaded: true, path: path, config: config, exists: exists, err: loadErr,
	}
	return config, exists, loadErr
}

// ResetCaptainConfigCache drops the memoized parse. It exists for specs that
// rewrite the config file in place under one path; the production path never
// needs it, because the file is read once at startup and the process that reads
// it is not the one that edits it.
func ResetCaptainConfigCache() {
	captainConfigMu.Lock()
	defer captainConfigMu.Unlock()
	captainConfigCache = captainConfigResult{}
}

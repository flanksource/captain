package cmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func CmuxBin() (string, error) {
	binary := os.Getenv("CMUX_BIN")
	if binary == "" {
		path, err := exec.LookPath("cmux")
		if err != nil && !errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("resolve cmux CLI: %w", err)
		}
		binary = path
		if errors.Is(err, exec.ErrNotFound) {
			binary = "/Applications/cmux.app/Contents/Resources/bin/cmux"
		}
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return "", fmt.Errorf("resolve cmux CLI %q: %w", binary, err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve cmux CLI symlinks %q: %w", path, err)
	}
	if strings.HasSuffix(filepath.ToSlash(resolved), ".app/Contents/MacOS/cmux") {
		path = filepath.Join(filepath.Dir(filepath.Dir(resolved)), "Resources", "bin", "cmux")
		path, err = exec.LookPath(path)
		if err != nil {
			return "", fmt.Errorf("resolve bundled cmux CLI for %q: %w", resolved, err)
		}
	}
	return path, nil
}

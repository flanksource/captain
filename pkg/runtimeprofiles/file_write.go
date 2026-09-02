package runtimeprofiles

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// writeFileAtomic replaces name through a temp file and rename so a failed
// write never leaves a half-written record behind. The temp name starts with a
// dot and ends in .tmp, both of which List skips.
func writeFileAtomic(root *os.Root, name string, data []byte) error {
	tmpName := "." + name + "." + rand.Text() + ".tmp"
	tmp, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	cleanup := func(err error) error {
		_ = tmp.Close()
		_ = root.Remove(tmpName)
		return fmt.Errorf("write %s: %w", name, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return cleanup(err)
	}
	if err := tmp.Close(); err != nil {
		return cleanup(err)
	}
	if err := root.Chmod(tmpName, 0o644); err != nil {
		return cleanup(err)
	}
	if err := root.Rename(tmpName, name); err != nil {
		return cleanup(err)
	}
	return nil
}

// slug lowercases a name and folds every run of other characters into one
// dash, yielding a key or an empty string.
func slug(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteRune('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// hashDir is the source id of a directory: stable across processes, so record
// ids survive a restart, and short enough to read in a URL.
func hashDir(dir string) string {
	sum := sha256.Sum256([]byte(dir))
	return hex.EncodeToString(sum[:])[:12]
}

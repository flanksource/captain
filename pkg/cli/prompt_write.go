package cli

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/clicky/entity"
)

type localPromptFile struct {
	root *os.Root
	rel  string
	path string
}

func openLocalPromptFile(record promptRecord, createRoot bool) (*localPromptFile, error) {
	rel, err := localPromptRel(record.Rel)
	if err != nil {
		return nil, err
	}
	if createRoot {
		if err := os.MkdirAll(record.Source.Root, 0o755); err != nil {
			return nil, fmt.Errorf("ensure prompt source: %w", err)
		}
	}
	root, err := os.OpenRoot(record.Source.Root)
	if err != nil {
		return nil, fmt.Errorf("open prompt source: %w", err)
	}
	return &localPromptFile{root: root, rel: rel, path: filepath.Join(record.Source.Root, rel)}, nil
}

func (file *localPromptFile) Close() error {
	return file.root.Close()
}

// promptVersion identifies one exact prompt content. The editor echoes it back
// as baseVersion on save so a file edited elsewhere since it was loaded is
// reported instead of overwritten.
func promptVersion(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:16]
}

// invalidPromptError is the 400 a write gets when its content cannot be rendered
// or inspected; nothing has been written when it is returned.
func invalidPromptError(err error) error {
	return entity.NewStatusErrorf(http.StatusBadRequest, "invalid_prompt", "invalid prompt: %v", err)
}

// checkPromptBaseVersion refuses to overwrite a prompt the caller loaded at a
// different version. An empty baseVersion (CLI callers) skips the check.
func checkPromptBaseVersion(record promptRecord, baseVersion string) error {
	if baseVersion == "" {
		return nil
	}
	current, err := readPromptContent(record)
	if err != nil {
		return err
	}
	if version := promptVersion(current); version != baseVersion {
		return entity.NewStatusErrorf(http.StatusConflict, "conflict",
			"prompt %s changed on disk since it was loaded (version %s, now %s); reload before saving",
			record.Rel, baseVersion, version)
	}
	return nil
}

// writePromptFileAtomic replaces the prompt through a temp file and rename so a
// failed write never leaves a half-written prompt behind. The temp name ends in
// .tmp, which the source walker ignores.
func writePromptFileAtomic(file *localPromptFile, content string) error {
	tmpRel := filepath.Join(filepath.Dir(file.rel), "."+filepath.Base(file.rel)+"."+rand.Text()+".tmp")
	tmp, err := file.root.OpenFile(tmpRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("write prompt: %w", err)
	}
	cleanup := func(err error) error {
		_ = tmp.Close()
		_ = file.root.Remove(tmpRel)
		return fmt.Errorf("write prompt: %w", err)
	}
	if _, err := tmp.WriteString(content); err != nil {
		return cleanup(err)
	}
	if err := tmp.Close(); err != nil {
		return cleanup(err)
	}
	if err := file.root.Chmod(tmpRel, 0o644); err != nil {
		return cleanup(err)
	}
	if err := file.root.Rename(tmpRel, file.rel); err != nil {
		return cleanup(err)
	}
	return nil
}

// promptRecordModTime reads the prompt's modification time for records that
// were resolved by id rather than discovered by the source walk.
func promptRecordModTime(record promptRecord) string {
	if record.Source.FS != nil {
		info, err := fs.Stat(record.Source.FS, record.Rel)
		if err != nil {
			return ""
		}
		return modTimeString(info)
	}
	file, err := openLocalPromptFile(record, false)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.root.Stat(file.rel)
	if err != nil {
		return ""
	}
	return modTimeString(info)
}

func modTimeString(info fs.FileInfo) string {
	if info == nil || info.ModTime().IsZero() {
		return ""
	}
	return info.ModTime().Format(time.RFC3339)
}

package migrations

import (
	"fmt"
	"io/fs"
	"path"
	"strings"
	"testing/fstest"

	commonsmigrate "github.com/flanksource/commons-db/migrate"
)

func schemaFilesystem(schemaName string) (fs.FS, error) {
	if err := commonsmigrate.ValidateSchemaName(schemaName); err != nil {
		return nil, fmt.Errorf("captain migration schema: %w", err)
	}
	if schemaName == DefaultSchema {
		return schemaFS, nil
	}
	files := fstest.MapFS{}
	err := fs.WalkDir(schemaFS, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, err := fs.ReadFile(schemaFS, name)
		if err != nil {
			return fmt.Errorf("read Captain migration %s: %w", name, err)
		}
		if strings.EqualFold(path.Ext(name), ".sql") {
			content = []byte(strings.ReplaceAll(string(content), DefaultSchema+".", schemaName+"."))
		}
		files[name] = &fstest.MapFile{Data: content}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("render Captain migration schema: %w", err)
	}
	return files, nil
}

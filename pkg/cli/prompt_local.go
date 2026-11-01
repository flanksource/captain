package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var errPromptLinkTarget = errors.New("must target a regular file without further links")

func listLocalPromptRecords(source promptSource) ([]promptRecord, error) {
	root, err := filepath.EvalSymlinks(source.Root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && source.Implicit {
			return nil, nil
		}
		return nil, fmt.Errorf("resolve prompt source %s: %w", source.Root, err)
	}
	var records []promptRecord
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if excludedPromptDirectory(entry.Name()) && (entry.IsDir() || entry.Type()&fs.ModeSymlink != 0) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			if filepath.Dir(rel) != "." {
				return nil
			}
			linked, err := linkedPromptRecords(source, rel)
			records = append(records, linked...)
			return err
		}
		if !entry.Type().IsRegular() || !strings.HasSuffix(rel, ".prompt") {
			return nil
		}
		record, err := localPromptRecord(source, rel)
		if err == nil {
			records = append(records, record)
		}
		return err
	})
	return records, err
}

func excludedPromptDirectory(name string) bool {
	return name == ".git" || name == "node_modules" || name == "vendor" || name == "dist"
}

func linkedPromptRecords(source promptSource, rel string) ([]promptRecord, error) {
	path := filepath.Join(source.Root, rel)
	info, err := os.Stat(path)
	if err != nil {
		return []promptRecord{invalidPromptLink(source, rel, fmt.Errorf("resolve prompt link %s: %w", path, err))}, nil
	}
	if !info.IsDir() {
		if !strings.HasSuffix(rel, ".prompt") {
			return nil, nil
		}
		record, err := localPromptRecord(source, rel)
		if err != nil {
			return nil, err
		}
		return []promptRecord{record}, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return []promptRecord{invalidPromptLink(source, rel, fmt.Errorf("read linked prompt directory %s: %w", path, err))}, nil
	}
	var records []promptRecord
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".prompt") {
			continue
		}
		record, err := localPromptRecord(source, filepath.Join(rel, entry.Name()))
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func localPromptRecord(source promptSource, rel string) (promptRecord, error) {
	rel, err := localPromptRel(rel)
	if err != nil {
		return promptRecord{}, err
	}
	record := promptRecord{
		Source: source, ID: encodePromptID(source.Kind, source.ID, filepath.ToSlash(rel)),
		Path: filepath.Join(source.Root, rel), Rel: filepath.ToSlash(rel),
	}
	root, err := os.OpenRoot(source.Root)
	if err != nil {
		return promptRecord{}, fmt.Errorf("open prompt source %s: %w", source.Root, err)
	}
	defer root.Close()
	parts := strings.Split(record.Rel, "/")
	var info fs.FileInfo
	for index := range parts {
		info, err = root.Lstat(filepath.Join(parts[:index+1]...))
		if err != nil {
			return promptRecord{}, fmt.Errorf("inspect prompt %s: %w", record.Path, err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			if index != 0 || len(parts) > 2 || excludedPromptDirectory(parts[0]) {
				return promptRecord{}, fmt.Errorf("prompt %s: links are only supported directly inside a prompt source, with immediate files only", record.Path)
			}
			linked, err := linkedPromptRecord(record)
			if err != nil && !errors.Is(err, errPromptLinkTarget) {
				return invalidPromptLink(source, rel, err), nil
			}
			return linked, err
		}
	}
	if !info.Mode().IsRegular() {
		return promptRecord{}, fmt.Errorf("prompt %s is not a regular file", record.Path)
	}
	record.UpdatedAt = modTimeString(info)
	return record, nil
}

func linkedPromptRecord(record promptRecord) (promptRecord, error) {
	parts := strings.Split(record.Rel, "/")
	target, err := filepath.EvalSymlinks(filepath.Join(record.Source.Root, parts[0]))
	if err != nil {
		return promptRecord{}, fmt.Errorf("resolve prompt link %s: %w", record.Path, err)
	}
	if len(parts) == 2 {
		target = filepath.Join(target, parts[1])
	}
	info, err := os.Lstat(target)
	if err != nil {
		return promptRecord{}, fmt.Errorf("inspect linked prompt %s: %w", record.Path, err)
	}
	if !info.Mode().IsRegular() {
		return promptRecord{}, fmt.Errorf("linked prompt %s %w", record.Path, errPromptLinkTarget)
	}
	record.ReadPath = target
	record.Source.Writable = false
	record.UpdatedAt = modTimeString(info)
	return record, nil
}

func openPromptReadFile(record promptRecord) (*localPromptFile, error) {
	if record.ReadPath == "" {
		return openLocalPromptFile(record, false)
	}
	root, err := os.OpenRoot(filepath.Dir(record.ReadPath))
	if err != nil {
		return nil, fmt.Errorf("open linked prompt %s: %w", record.Path, err)
	}
	return &localPromptFile{root: root, rel: filepath.Base(record.ReadPath), path: record.Path}, nil
}

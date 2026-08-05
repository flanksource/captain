package cli

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
)

type AttachmentsGCOptions struct {
	DryRun bool `flag:"dry-run" help:"Report eligible attachments without deleting them"`
}

func RunAttachmentsGC(opts AttachmentsGCOptions) (any, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}
	store, err := newAttachmentStore(cwd)
	if err != nil {
		return nil, err
	}
	retention, err := parseAttachmentRetention(loadSavedConfig().Attachments.WithDefaults().Retention)
	if err != nil {
		return nil, err
	}
	referenced, err := collectAttachmentReferences(filepath.Join(cwd, ".captain"), store.Directory())
	if err != nil {
		return nil, err
	}
	databaseReferences, err := collectDatabaseAttachmentReferences(context.Background())
	if err != nil {
		return nil, err
	}
	for id := range databaseReferences {
		referenced[id] = struct{}{}
	}
	return store.GC(referenced, retention, opts.DryRun)
}

func parseAttachmentRetention(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if days, ok := strings.CutSuffix(value, "d"); ok {
		count, err := strconv.Atoi(days)
		if err != nil || count < 1 {
			return 0, fmt.Errorf("invalid attachment retention %q: use a positive duration such as 30d", value)
		}
		return time.Duration(count) * 24 * time.Hour, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("invalid attachment retention %q: use a positive duration such as 30d", value)
	}
	return duration, nil
}

var attachmentIDPattern = regexp.MustCompile(regexp.QuoteMeta(api.AttachmentIDPrefix) + `[0-9a-fA-F]{64}`)

func collectAttachmentReferences(root, storeDirectory string) (map[string]struct{}, error) {
	references := map[string]struct{}{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			if path == storeDirectory {
				return filepath.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read attachment reference source %s: %w", path, err)
		}
		for id := range attachmentReferencesFromContents([]string{string(data)}) {
			references[id] = struct{}{}
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("scan attachment references: %w", err)
	}
	return references, nil
}

func collectDatabaseAttachmentReferences(ctx context.Context) (map[string]struct{}, error) {
	// GC deletes attachments captain owns, so it only ever consults the
	// database captain writes.
	db, err := captainDefaultDB(ctx)
	if err != nil {
		return nil, fmt.Errorf("open attachment reference database: %w", err)
	}
	var rows []struct {
		Content string
	}
	if err := db.Gorm().WithContext(ctx).Raw(`
		SELECT rendered_spec::text AS content FROM captain_prompt_runs
		UNION ALL
		SELECT payload::text AS content FROM captain_events
	`).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("scan database attachment references: %w", err)
	}
	contents := make([]string, len(rows))
	for i := range rows {
		contents[i] = rows[i].Content
	}
	return attachmentReferencesFromContents(contents), nil
}

func attachmentReferencesFromContents(contents []string) map[string]struct{} {
	references := map[string]struct{}{}
	for _, content := range contents {
		for _, match := range attachmentIDPattern.FindAllString(content, -1) {
			references[strings.ToLower(match)] = struct{}{}
		}
	}
	return references
}

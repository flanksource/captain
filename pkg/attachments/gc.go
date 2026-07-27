package attachments

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
)

type GCResult struct {
	RemovedIDs    []string `json:"removedIds" pretty:"label=Attachments"`
	RemovedBytes  int64    `json:"removedBytes" pretty:"label=Bytes"`
	DryRun        bool     `json:"dryRun" pretty:"label=Dry Run"`
	RetentionDays int      `json:"retentionDays" pretty:"label=Retention Days"`
}

func (s *Store) GC(referenced map[string]struct{}, retention time.Duration, dryRun bool) (GCResult, error) {
	if retention <= 0 {
		return GCResult{}, fmt.Errorf("attachment retention must be positive")
	}
	result := GCResult{DryRun: dryRun, RetentionDays: int(retention.Hours() / 24)}
	cutoff := time.Now().Add(-retention)
	root := filepath.Join(s.directory, "sha256")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".attachment-") {
			return nil
		}
		id := api.AttachmentIDPrefix + entry.Name()
		if _, ok := referenced[id]; ok {
			return nil
		}
		ref := api.AttachmentRef{ID: id}
		if ref.Validate() != nil {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.ModTime().Before(cutoff) {
			return nil
		}
		result.RemovedIDs = append(result.RemovedIDs, id)
		result.RemovedBytes += info.Size()
		if !dryRun {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("remove attachment %s: %w", id, err)
			}
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return GCResult{}, fmt.Errorf("scan attachment store: %w", err)
	}
	sort.Strings(result.RemovedIDs)
	return result, nil
}

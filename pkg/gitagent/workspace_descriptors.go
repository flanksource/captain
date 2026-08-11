package gitagent

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

func markOpenDescriptorsCloseOnExec(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("marking inherited descriptors close-on-exec via %s: %w", path, err)
	}
	defer dir.Close()
	entries, err := dir.Readdirnames(-1)
	if err != nil {
		return fmt.Errorf("listing inherited descriptors via %s: %w", path, err)
	}
	for _, entry := range entries {
		fd, parseErr := strconv.Atoi(entry)
		if parseErr == nil && fd >= 3 {
			syscall.CloseOnExec(fd)
		}
	}
	return nil
}

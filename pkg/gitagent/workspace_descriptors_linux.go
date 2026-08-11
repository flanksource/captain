package gitagent

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// markInheritedDescriptorsCloseOnExec prevents receive-pack's sideband pipes
// from surviving in the detached agent and keeping the dispatch push open.
func markInheritedDescriptorsCloseOnExec() error {
	closeRangeErr := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC)
	if closeRangeErr == nil {
		return nil
	}
	if err := markOpenDescriptorsCloseOnExec("/proc/self/fd"); err != nil {
		return fmt.Errorf("close_range failed: %v; %w", closeRangeErr, err)
	}
	return nil
}

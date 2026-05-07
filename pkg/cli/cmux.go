package cli

import (
	"fmt"

	"github.com/flanksource/captain/pkg/cmux"
)

type CmuxScreenshotOptions struct{}

func RunCmuxScreenshot(opts CmuxScreenshotOptions) (any, error) {
	path, err := cmux.Screenshot()
	if err != nil {
		return nil, err
	}

	if err := cmux.CopyToClipboard(path); err != nil {
		fmt.Printf("%s\n", path)
		return path, nil
	}

	fmt.Printf("%s (copied to clipboard)\n", path)
	return path, nil
}

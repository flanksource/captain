package deliver

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/ai/provider/cmux"
)

func (d Deliverer) submitCmux(ctx context.Context, target Target, text string) error {
	client := d.Cmux
	if client == nil {
		client = cmux.NewClient("")
	}
	ref := cmux.WorkspaceRef{WorkspaceID: target.Surface.WorkspaceID, SurfaceID: target.Surface.SurfaceID}
	if err := cmux.SubmitToSurface(ctx, client, ref, target.SessionFile, text); err != nil {
		return fmt.Errorf("type into cmux surface %s: %w", target.Surface.SurfaceID, err)
	}
	return nil
}

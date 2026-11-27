package deliver

import (
	"context"
	"errors"
	"fmt"

	"github.com/flanksource/captain/pkg/ai/provider/cmux"
)

// messageSender is the --from name the agent sees on a cmux agent message. It
// is fixed so delivery never depends on the sender itself running in cmux.
const messageSender = "captain"

func (d Deliverer) messageCmux(ctx context.Context, target Target, text string) error {
	client := d.Cmux
	if client == nil {
		client = cmux.NewClient("")
	}
	err := client.AgentMessage(ctx, target.Surface.SurfaceID, messageSender, text)
	switch {
	case errors.Is(err, cmux.ErrNotStarted):
		// cmux received nothing; any other failure may follow cmux accepting the
		// message, so only this one is ErrNoChannel and permits a resume.
		return fmt.Errorf("%w: cmux agent message to surface %s: %w", ErrNoChannel, target.Surface.SurfaceID, err)
	case err != nil:
		return fmt.Errorf("cmux agent message to surface %s: %w", target.Surface.SurfaceID, err)
	}
	return nil
}

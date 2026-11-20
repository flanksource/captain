package deliver

import (
	"context"
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
	if err := client.AgentMessage(ctx, target.Surface.SurfaceID, messageSender, text); err != nil {
		return fmt.Errorf("cmux agent message to surface %s: %w", target.Surface.SurfaceID, err)
	}
	return nil
}

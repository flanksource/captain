package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/flanksource/captain/pkg/captainconfig"
)

// startCredentialPublisher runs the credential republish loop for the lifetime
// of `captain serve`.
//
// This is the supervisor half of the sandbox-credentials feature: the sandbox
// receives an access token with no refresh token, so it cannot renew itself and
// something here has to. It sits beside the session monitor rather than inside
// it — the monitor's backfill pass runs on a 24h interval, which is two orders
// of magnitude too slow for a credential that lapses within the hour.
//
// Publishing is opt-in: with no credentials.publish entries this does nothing
// and the server starts as before.
func startCredentialPublisher(ctx context.Context, stdout io.Writer) error {
	saved, _, err := captainconfig.Load()
	if err != nil {
		return err
	}
	if len(saved.Credentials.Publish) == 0 {
		return nil
	}
	// A malformed destination is a startup error, not a warning: a supervisor
	// that comes up having silently published nothing looks healthy while every
	// agent it serves fails to authenticate.
	if err := saved.Credentials.Validate(); err != nil {
		return err
	}

	publisher, err := buildCredentialPublisher(CredentialsOptions{})
	if err != nil {
		return err
	}

	names := make([]string, 0, len(publisher.Targets))
	for _, target := range publisher.Targets {
		names = append(names, target.Name())
	}
	fmt.Fprintf(stdout, "  credentials:  %d provider(s) -> %v\n", len(publisher.Providers), names)

	go func() {
		if err := publisher.Run(ctx); err != nil && ctx.Err() == nil {
			log.Errorf("credential publisher stopped: %v", err)
		}
	}()
	return nil
}

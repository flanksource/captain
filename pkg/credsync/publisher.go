// Package credsync keeps the agent CLIs' redacted subscription logins fresh
// wherever sandboxed workloads read them — a shared folder for Docker, a
// Kubernetes Secret for a deployed sidecar.
//
// It exists because the redaction that makes those credentials safe to hand out
// also makes them short-lived: the sandbox holds an access token with no
// refresh token, so it cannot renew itself. Something on the supervisor has to,
// and the schedule is driven by the credential's own expiry rather than a fixed
// interval, so what lands in the target is as fresh as the host can make it.
package credsync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/agentcreds"
	"github.com/flanksource/commons/logger"
)

var log = logger.GetLogger("credsync")

// Target is one place credentials are published to.
type Target interface {
	// Name identifies the target in logs and status output.
	Name() string
	Publish(ctx context.Context, credentials []agentcreds.Credential) error
}

// Scheduling bounds. The interval between publishes is derived from the
// credential's expiry, then clamped: the floor stops a credential that is
// always near expiry from becoming a hot loop, and the ceiling keeps a
// long-lived token (a Codex access token runs for days) on a cadence that still
// notices a host re-login promptly.
const (
	DefaultMargin   = 5 * time.Minute
	MinimumInterval = time.Minute
	MaximumInterval = 30 * time.Minute
	// RetryInterval is how soon a failed publish is retried. Shorter than
	// MinimumInterval because a failure usually means a stale source that a
	// human is about to refresh by using the CLI.
	RetryInterval = 2 * time.Minute
)

// Publisher reads the host's logins and pushes the redacted result to targets.
type Publisher struct {
	Reader    agentcreds.Reader
	Providers []agentcreds.Provider
	Targets   []Target
	// Margin is how far before expiry a republish is scheduled.
	Margin time.Duration
	Now    func() time.Time
}

// Result records one publish attempt, for `captain sandbox credentials status`.
type Result struct {
	Published []PublishedCredential `json:"published" pretty:"label=Credentials"`
	Targets   []string              `json:"targets" pretty:"label=Targets"`
	// NextPublish is when the loop will run again.
	NextPublish time.Time `json:"nextPublish" pretty:"label=Next publish"`
}

// PublishedCredential is one credential's identity and lifetime. It never
// carries the credential itself — status output is not a way to read a token.
type PublishedCredential struct {
	Provider  string    `json:"provider" pretty:"label=Provider"`
	Key       string    `json:"key" pretty:"label=Key"`
	Bytes     int       `json:"bytes" pretty:"label=Size"`
	ExpiresAt time.Time `json:"expiresAt" pretty:"label=Expires"`
}

func (p Publisher) now() time.Time {
	if p.Now == nil {
		return time.Now()
	}
	return p.Now()
}

func (p Publisher) margin() time.Duration {
	if p.Margin <= 0 {
		return DefaultMargin
	}
	return p.Margin
}

// PublishOnce reads every configured provider and writes to every target.
//
// An expired source is refused rather than published: the existing target keeps
// whatever it already holds, because a dead token in a Secret turns a problem
// the supervisor can see and name into a 401 inside an agent hours later.
func (p Publisher) PublishOnce(ctx context.Context) (Result, error) {
	if len(p.Providers) == 0 {
		return Result{}, fmt.Errorf("no credential providers configured")
	}
	if len(p.Targets) == 0 {
		return Result{}, fmt.Errorf("no credential targets configured")
	}

	credentials, err := p.Reader.ReadAll(ctx, p.Providers)
	if err != nil {
		return Result{}, err
	}
	now := p.now()
	for _, credential := range credentials {
		if credential.Expired(now) {
			return Result{}, fmt.Errorf(
				"%s credential expired %s ago; run `%s` on this host to refresh it (nothing was published, the previous credential is untouched)",
				credential.Provider, now.Sub(credential.ExpiresAt).Round(time.Second),
				refreshCommand(credential.Provider))
		}
	}

	result := Result{NextPublish: now.Add(p.interval(credentials, now))}
	for _, credential := range credentials {
		result.Published = append(result.Published, PublishedCredential{
			Provider:  string(credential.Provider),
			Key:       credential.Filename,
			Bytes:     len(credential.Payload),
			ExpiresAt: credential.ExpiresAt,
		})
	}

	var errs []error
	for _, target := range p.Targets {
		if err := target.Publish(ctx, credentials); err != nil {
			errs = append(errs, err)
			continue
		}
		result.Targets = append(result.Targets, target.Name())
	}
	if len(errs) > 0 {
		return result, errors.Join(errs...)
	}
	return result, nil
}

// Run publishes now and then keeps republishing ahead of expiry until ctx ends.
func (p Publisher) Run(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}

		wait := RetryInterval
		result, err := p.PublishOnce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Warnf("Credential publish failed, retrying in %s: %v", wait, err)
		} else {
			wait = time.Until(result.NextPublish)
			if wait < MinimumInterval {
				wait = MinimumInterval
			}
			log.Infof("Published %d credential(s) to %d target(s); next publish in %s",
				len(result.Published), len(result.Targets), wait.Round(time.Second))
		}
		timer.Reset(wait)
	}
}

// interval is how long until the next publish: enough before the earliest
// expiry to leave the margin, clamped to the bounds above.
func (p Publisher) interval(credentials []agentcreds.Credential, now time.Time) time.Duration {
	earliest := time.Time{}
	for _, credential := range credentials {
		if earliest.IsZero() || credential.ExpiresAt.Before(earliest) {
			earliest = credential.ExpiresAt
		}
	}
	if earliest.IsZero() {
		return MaximumInterval
	}
	interval := earliest.Sub(now) - p.margin()
	if interval < MinimumInterval {
		return MinimumInterval
	}
	if interval > MaximumInterval {
		return MaximumInterval
	}
	return interval
}

// refreshCommand names what a human should run to renew a lapsed login. The
// CLIs refresh their own tokens when used, so "use it" is the actual fix.
func refreshCommand(provider agentcreds.Provider) string {
	if provider == agentcreds.ProviderCodex {
		return "codex login"
	}
	return "claude"
}

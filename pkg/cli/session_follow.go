package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	sessionquery "github.com/flanksource/captain/pkg/session/query"
	"golang.org/x/term"
)

// followSessionGet prints a session's transcript and then every message as it
// arrives, until the session reaches a terminal lifecycle or ctx ends. Filters
// and windows describe a finished read, so they are refused rather than
// silently ignored.
func followSessionGet(ctx context.Context, db *database.DB, opts SessionGetOptions, output io.Writer) error {
	if len(opts.Tools) > 0 || len(opts.Categories) > 0 || opts.Tail > 0 || opts.Offset > 0 {
		return errors.New("--follow streams the whole transcript; --tool, --category, --tail and --offset do not apply to it")
	}
	events, err := sessionquery.Follow(ctx, db, opts.ID, sessionquery.FollowOptions{Replay: true})
	if err != nil {
		return err
	}
	ansi := isTerminalWriter(output)
	for event := range events {
		var text string
		switch {
		case event.Err != nil:
			return event.Err
		case event.Message != nil:
			for _, row := range (&session.Session{Messages: []session.Message{*event.Message}}).TranscriptRows() {
				text += renderFollowLine(row.Pretty(), ansi)
			}
		case event.State != nil:
			text = fmt.Sprintf("-- session %s (%s, revision %d, facets %s)\n",
				event.State.LifecycleStatus, event.State.ActivityState, event.State.Revision, event.State.Facets)
		}
		if _, err := io.WriteString(output, text); err != nil {
			return fmt.Errorf("write followed session %s: %w", opts.ID, err)
		}
	}
	return ctx.Err()
}

func renderFollowLine(text interface {
	ANSI() string
	String() string
}, ansi bool) string {
	if ansi {
		return text.ANSI() + "\n"
	}
	return text.String() + "\n"
}

func isTerminalWriter(output io.Writer) bool {
	file, ok := output.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

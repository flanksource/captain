package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/flanksource/commons/logger"
	"github.com/jackc/pgx/v5/stdlib"
)

// relistenBackoff bounds how long a lost LISTEN connection is retried before
// Run gives up. Notifications sent while it is down are lost, which is why
// every successful re-LISTEN is reported to the caller.
var relistenBackoff = []time.Duration{
	100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second,
}

const unlistenTimeout = 5 * time.Second

// channelName is what LISTEN accepts unquoted. The statement is sent verbatim
// so pg_stat_activity shows exactly `LISTEN <channel>`.
var channelName = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Listener is a dedicated pool connection LISTENing on one PostgreSQL channel.
// It is the shared seam behind every Captain notification consumer: the
// session follower (captain_session_change) and host projections
// (captain_row_change).
type Listener struct {
	pool    *sql.DB
	channel string
	conn    *sql.Conn
}

// Listen takes a dedicated connection out of pool and LISTENs on channel. A
// LISTEN that cannot be established is an error: there is no polling fallback.
// The pool must use the pgx stdlib driver.
func Listen(ctx context.Context, pool *sql.DB, channel string) (*Listener, error) {
	if pool == nil {
		return nil, fmt.Errorf("LISTEN %s: a connection pool is required", channel)
	}
	if !channelName.MatchString(channel) {
		return nil, fmt.Errorf("LISTEN: invalid channel name %q", channel)
	}
	listener := &Listener{pool: pool, channel: channel}
	conn, err := listener.listen(ctx)
	if err != nil {
		return nil, err
	}
	listener.conn = conn
	return listener, nil
}

// Run delivers every notification payload to onNotify until ctx is cancelled,
// onNotify returns an error, or a lost connection cannot be re-established
// within the backoff. A lost connection is replaced and re-LISTENed, then
// onReconnect is called so the caller can catch up on what it missed. Run
// always releases the connection before returning; a cancelled ctx returns
// ctx.Err().
func (l *Listener) Run(ctx context.Context, onNotify func(payload string) error, onReconnect func() error) error {
	if onNotify == nil || onReconnect == nil {
		l.release()
		return fmt.Errorf("LISTEN %s: notify and reconnect callbacks are required", l.channel)
	}
	for {
		waitErr := l.wait(ctx, onNotify)
		var handlerErr *notifyHandlerError
		if ctx.Err() != nil || errors.As(waitErr, &handlerErr) {
			l.release()
			if handlerErr != nil {
				return handlerErr.err
			}
			return ctx.Err()
		}
		logger.Warnf("captain LISTEN %s: connection lost, re-listening: %v", l.channel, waitErr)
		// wait reported the lost connection as ErrBadConn, so database/sql has
		// already closed and discarded it.
		conn, err := l.relisten(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("re-establish LISTEN %s: %w", l.channel, err)
		}
		l.conn = conn
		if err := onReconnect(); err != nil {
			l.release()
			return err
		}
	}
}

type notifyHandlerError struct{ err error }

func (e *notifyHandlerError) Error() string { return e.err.Error() }

func (l *Listener) relisten(ctx context.Context) (*sql.Conn, error) {
	var lastErr error
	for _, delay := range relistenBackoff {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		conn, err := l.listen(ctx)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (l *Listener) listen(ctx context.Context) (*sql.Conn, error) {
	conn, err := l.pool.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire LISTEN connection for %s: %w", l.channel, err)
	}
	err = conn.Raw(func(driverConn any) error {
		pgxConn, ok := driverConn.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("captain LISTEN requires the pgx stdlib driver, got %T", driverConn)
		}
		_, execErr := pgxConn.Conn().Exec(ctx, "LISTEN "+l.channel)
		return execErr
	})
	if err != nil {
		closeErr := conn.Close()
		return nil, errors.Join(fmt.Errorf("LISTEN %s: %w", l.channel, err), closeErr)
	}
	return conn, nil
}

// wait blocks in WaitForNotification until ctx is cancelled, onNotify fails,
// or the connection fails. A failed connection is reported as ErrBadConn so
// database/sql discards it instead of returning it to the pool.
func (l *Listener) wait(ctx context.Context, onNotify func(string) error) error {
	return l.conn.Raw(func(driverConn any) error {
		pgxConn := driverConn.(*stdlib.Conn).Conn()
		for {
			notification, err := pgxConn.WaitForNotification(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("%w: %w", driver.ErrBadConn, err)
			}
			if notification.Channel != l.channel {
				continue
			}
			if err := onNotify(notification.Payload); err != nil {
				return &notifyHandlerError{err: err}
			}
		}
	})
}

// release UNLISTENs and returns the connection to the pool. A connection that
// cannot UNLISTEN is discarded rather than pooled, so no pooled connection ever
// keeps a stale subscription.
func (l *Listener) release() {
	ctx, cancel := context.WithTimeout(context.Background(), unlistenTimeout)
	defer cancel()
	if _, err := l.conn.ExecContext(ctx, "UNLISTEN "+l.channel); err != nil {
		logger.Warnf("captain LISTEN %s: UNLISTEN failed, discarding connection: %v", l.channel, err)
		_ = l.conn.Raw(func(any) error { return driver.ErrBadConn })
		return
	}
	if err := l.conn.Close(); err != nil {
		logger.Warnf("captain LISTEN %s: release connection: %v", l.channel, err)
	}
}

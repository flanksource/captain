package query

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons/logger"
	"github.com/jackc/pgx/v5/stdlib"
)

// SessionChangeChannel is the PostgreSQL channel migration 83 notifies with a
// session UUID whenever a session's transcript, state or plan changes.
const SessionChangeChannel = "captain_session_change"

// relistenBackoff bounds how long a lost listener connection is retried before
// every subscriber is failed. Notifications sent while it is down are lost, so
// a reconnect is always followed by one catch-up signal to every subscriber.
var relistenBackoff = []time.Duration{
	100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second,
}

const unlistenTimeout = 5 * time.Second

var followHubs = struct {
	sync.Mutex
	byPool map[*sql.DB]*followHub
}{byPool: map[*sql.DB]*followHub{}}

// followHub owns one dedicated LISTEN connection per database pool and fans
// each notification out to the subscribers of that session id. The connection
// is taken lazily by the first subscriber and released by the last.
type followHub struct {
	pool     *sql.DB
	mu       sync.Mutex
	subs     map[*sessionSubscription]struct{}
	byID     map[string]map[*sessionSubscription]struct{}
	listener *hubListener
}

type hubListener struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// sessionSubscription wakes a follower. signal coalesces: a follower re-reads
// everything it follows on each wake-up, so one pending signal is enough.
type sessionSubscription struct {
	hub    *followHub
	ids    map[string]struct{}
	signal chan struct{}
	failed chan error
}

func hubFor(db *database.DB) (*followHub, error) {
	if db == nil || db.Gorm() == nil {
		return nil, errors.New("follow Captain session: a database is required")
	}
	pool, err := db.Gorm().DB()
	if err != nil {
		return nil, fmt.Errorf("follow Captain session: database handle has no connection pool: %w", err)
	}
	followHubs.Lock()
	defer followHubs.Unlock()
	hub := followHubs.byPool[pool]
	if hub == nil {
		hub = &followHub{
			pool: pool,
			subs: map[*sessionSubscription]struct{}{},
			byID: map[string]map[*sessionSubscription]struct{}{},
		}
		followHubs.byPool[pool] = hub
	}
	return hub, nil
}

// subscribe registers a subscriber for ids, establishing the LISTEN connection
// when it is the first. A LISTEN that cannot be established is an error: there
// is no polling fallback.
func (h *followHub) subscribe(ctx context.Context, ids []string) (*sessionSubscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.listener == nil {
		conn, err := h.listen(ctx)
		if err != nil {
			return nil, err
		}
		listenCtx, cancel := context.WithCancel(context.Background())
		h.listener = &hubListener{cancel: cancel, done: make(chan struct{})}
		go h.run(listenCtx, h.listener, conn)
	}
	sub := &sessionSubscription{
		hub: h, ids: map[string]struct{}{},
		signal: make(chan struct{}, 1), failed: make(chan error, 1),
	}
	h.subs[sub] = struct{}{}
	h.setIDsLocked(sub, ids)
	return sub, nil
}

// follow replaces the session ids the subscriber is woken for.
func (s *sessionSubscription) follow(ids []string) {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	if _, ok := s.hub.subs[s]; ok {
		s.hub.setIDsLocked(s, ids)
	}
}

// close unregisters the subscriber. The last one out stops the listener and
// returns its connection to the pool before close returns.
func (s *sessionSubscription) close() {
	h := s.hub
	h.mu.Lock()
	if _, ok := h.subs[s]; !ok {
		h.mu.Unlock()
		return
	}
	h.setIDsLocked(s, nil)
	delete(h.subs, s)
	var stopping *hubListener
	if len(h.subs) == 0 && h.listener != nil {
		stopping, h.listener = h.listener, nil
	}
	h.mu.Unlock()
	if stopping != nil {
		stopping.cancel()
		<-stopping.done
	}
}

func (h *followHub) setIDsLocked(sub *sessionSubscription, ids []string) {
	for id := range sub.ids {
		delete(h.byID[id], sub)
		if len(h.byID[id]) == 0 {
			delete(h.byID, id)
		}
	}
	sub.ids = make(map[string]struct{}, len(ids))
	for _, id := range ids {
		sub.ids[id] = struct{}{}
		if h.byID[id] == nil {
			h.byID[id] = map[*sessionSubscription]struct{}{}
		}
		h.byID[id][sub] = struct{}{}
	}
}

func (h *followHub) dispatch(sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.byID[sessionID] {
		sub.wake()
	}
}

func (h *followHub) wakeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		sub.wake()
	}
}

// failAll reports a listener that could not be re-established to every
// subscriber it served, and detaches it so the next subscriber starts afresh.
func (h *followHub) failAll(listener *hubListener, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.listener == listener {
		h.listener = nil
	}
	for sub := range h.subs {
		select {
		case sub.failed <- err:
		default:
		}
	}
}

func (s *sessionSubscription) wake() {
	select {
	case s.signal <- struct{}{}:
	default:
	}
}

// run waits for notifications until ctx is cancelled. A lost connection is
// replaced and re-LISTENed, then every subscriber gets one catch-up signal.
func (h *followHub) run(ctx context.Context, listener *hubListener, conn *sql.Conn) {
	defer close(listener.done)
	for {
		err := waitForSessionChanges(ctx, conn, h.dispatch)
		if ctx.Err() != nil {
			releaseListenConn(conn)
			return
		}
		logger.Warnf("captain session follow: listener connection lost, re-listening: %v", err)
		conn, err = h.relisten(ctx)
		if ctx.Err() != nil {
			if conn != nil {
				releaseListenConn(conn)
			}
			return
		}
		if err != nil {
			h.failAll(listener, fmt.Errorf("re-establish LISTEN %s: %w", SessionChangeChannel, err))
			return
		}
		h.wakeAll()
	}
}

func (h *followHub) relisten(ctx context.Context) (*sql.Conn, error) {
	var lastErr error
	for _, delay := range relistenBackoff {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		conn, err := h.listen(ctx)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// listen takes a dedicated connection out of the pool and LISTENs on it.
func (h *followHub) listen(ctx context.Context) (*sql.Conn, error) {
	conn, err := h.pool.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire LISTEN connection for %s: %w", SessionChangeChannel, err)
	}
	err = conn.Raw(func(driverConn any) error {
		pgxConn, ok := driverConn.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("captain session follow requires the pgx stdlib driver, got %T", driverConn)
		}
		_, execErr := pgxConn.Conn().Exec(ctx, "LISTEN "+SessionChangeChannel)
		return execErr
	})
	if err != nil {
		closeErr := conn.Close()
		return nil, errors.Join(fmt.Errorf("LISTEN %s: %w", SessionChangeChannel, err), closeErr)
	}
	return conn, nil
}

// waitForSessionChanges blocks in WaitForNotification until ctx is cancelled
// or the connection fails. A failed connection is reported as ErrBadConn so
// database/sql discards it instead of returning it to the pool.
func waitForSessionChanges(ctx context.Context, conn *sql.Conn, dispatch func(string)) error {
	return conn.Raw(func(driverConn any) error {
		pgxConn := driverConn.(*stdlib.Conn).Conn()
		for {
			notification, err := pgxConn.WaitForNotification(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("%w: %w", driver.ErrBadConn, err)
			}
			if notification.Channel == SessionChangeChannel {
				dispatch(notification.Payload)
			}
		}
	})
}

// releaseListenConn UNLISTENs and returns the connection to the pool. A
// connection that cannot UNLISTEN is discarded rather than pooled, so no pooled
// connection ever keeps a stale subscription.
func releaseListenConn(conn *sql.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), unlistenTimeout)
	defer cancel()
	if _, err := conn.ExecContext(ctx, "UNLISTEN "+SessionChangeChannel); err != nil {
		logger.Warnf("captain session follow: UNLISTEN failed, discarding connection: %v", err)
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return
	}
	if err := conn.Close(); err != nil {
		logger.Warnf("captain session follow: release LISTEN connection: %v", err)
	}
}

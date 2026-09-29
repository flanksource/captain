package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/flanksource/captain/pkg/database"
)

// SessionChangeChannel is the PostgreSQL channel migration 83 notifies with a
// session UUID whenever a session's transcript, state or plan changes.
const SessionChangeChannel = "captain_session_change"

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
		conn, err := database.Listen(ctx, h.pool, SessionChangeChannel)
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

// run waits for notifications until ctx is cancelled. The shared listener
// replaces and re-LISTENs a lost connection, then every subscriber gets one
// catch-up signal; one that cannot be re-established fails every subscriber.
func (h *followHub) run(ctx context.Context, listener *hubListener, conn *database.Listener) {
	defer close(listener.done)
	err := conn.Run(ctx,
		func(sessionID string) error { h.dispatch(sessionID); return nil },
		func() error { h.wakeAll(); return nil },
	)
	if ctx.Err() != nil {
		return
	}
	h.failAll(listener, err)
}

package monitor

import (
	"container/list"
	"context"
	"fmt"
	"sync"

	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
)

// sessionIDCacheSize bounds the identity cache. Far more sessions than one
// host runs concurrently, so a live session is never evicted by others.
const sessionIDCacheSize = 4096

type sessionIdentity struct {
	source, hostID, providerSessionID string
}

type sessionIDEntry struct {
	identity sessionIdentity
	id       uuid.UUID
}

// sessionIDCache is a bounded LRU from a provider identity to its Captain
// session UUID. The UUID a provider identity resolves to never changes once
// created, so an entry is never wrong while its session exists; a deleted
// session is dropped with forgetSession when a write against it fails.
type sessionIDCache struct {
	mu       sync.Mutex
	capacity int
	order    *list.List // front is most recently used; values are sessionIDEntry
	entries  map[sessionIdentity]*list.Element
}

func newSessionIDCache(capacity int) *sessionIDCache {
	return &sessionIDCache{capacity: capacity, order: list.New(), entries: map[sessionIdentity]*list.Element{}}
}

func (c *sessionIDCache) get(identity sessionIdentity) (uuid.UUID, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[identity]
	if !ok {
		return uuid.Nil, false
	}
	c.order.MoveToFront(element)
	return element.Value.(sessionIDEntry).id, true
}

func (c *sessionIDCache) put(identity sessionIdentity, id uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[identity]; ok {
		element.Value = sessionIDEntry{identity: identity, id: id}
		c.order.MoveToFront(element)
		return
	}
	c.entries[identity] = c.order.PushFront(sessionIDEntry{identity: identity, id: id})
	if c.order.Len() > c.capacity {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(sessionIDEntry).identity)
	}
}

func (c *sessionIDCache) forgetSession(id uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for element := c.order.Front(); element != nil; element = element.Next() {
		if entry := element.Value.(sessionIDEntry); entry.id == id {
			c.order.Remove(element)
			delete(c.entries, entry.identity)
			return
		}
	}
}

// providerSessionID resolves a provider identity to its session UUID, creating
// the session on first sight. The status-line hook and the process poll ask
// for the same few live sessions every second, and only the UUID is needed, so
// a cache hit issues no query at all. Only a plain identity is cacheable: on
// an existing row CreateOrGetSession also adopts a provider label, hierarchy,
// or metadata, and a cache hit would silently skip that.
func (m *Monitor) providerSessionID(ctx context.Context, input database.CreateSessionInput) (uuid.UUID, error) {
	if input.ProviderSessionID == "" || input.Provider != "" || input.ParentSessionID != nil ||
		input.RootSessionID != nil || len(input.Metadata) > 0 {
		return uuid.Nil, fmt.Errorf("resolve %s session %q: only a plain provider identity is cacheable", input.Source, input.ProviderSessionID)
	}
	identity := sessionIdentity{source: input.Source, hostID: input.HostID, providerSessionID: input.ProviderSessionID}
	if id, ok := m.sessionIDs.get(identity); ok {
		return id, nil
	}
	session, err := m.db.CreateOrGetSession(ctx, input)
	if err != nil {
		return uuid.Nil, err
	}
	m.sessionIDs.put(identity, session.ID)
	return session.ID, nil
}

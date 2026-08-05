package cli

import (
	"context"
	"sync"

	"github.com/flanksource/captain/pkg/aichat"
)

// chatThreadStores memoizes one chat thread store per database context, so a
// request reading a secondary context lists that database's threads instead of
// the monitored one's.
var chatThreadStores struct {
	mu     sync.Mutex
	byName map[string]aichat.ThreadStore
}

// contextThreadStore resolves the chat thread store for the request's database
// context. Thread writes never reach it on a secondary context: the database
// context middleware rejects unsafe methods first.
func contextThreadStore(ctx context.Context) (aichat.ThreadStore, error) {
	name := activeDatabaseContextName(ctx)

	chatThreadStores.mu.Lock()
	store, ok := chatThreadStores.byName[name]
	chatThreadStores.mu.Unlock()
	if ok {
		return store, nil
	}

	db, err := openContextDB(ctx, name, captainDatabaseNoMigrations)
	if err != nil {
		return nil, err
	}
	store, err = aichat.NewDatabaseThreadStore(db)
	if err != nil {
		return nil, err
	}

	chatThreadStores.mu.Lock()
	defer chatThreadStores.mu.Unlock()
	if existing, ok := chatThreadStores.byName[name]; ok {
		return existing, nil
	}
	if chatThreadStores.byName == nil {
		chatThreadStores.byName = map[string]aichat.ThreadStore{}
	}
	chatThreadStores.byName[name] = store
	return store, nil
}

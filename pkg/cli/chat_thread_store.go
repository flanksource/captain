package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/flanksource/clicky/aichat"
)

type fileThreadStore struct {
	path string
	mu   sync.Mutex
}

type threadStoreFile struct {
	Threads []*aichat.Thread `json:"threads"`
}

func newFileThreadStore(path string) *fileThreadStore {
	return &fileThreadStore{path: path}
}

func (s *fileThreadStore) Create(_ context.Context, title string) (*aichat.Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	if title == "" {
		title = "New conversation"
	}
	now := time.Now()
	thread := &aichat.Thread{
		ID:        newThreadID(),
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
	}
	state.Threads = append(state.Threads, thread)
	if err := s.saveLocked(state); err != nil {
		return nil, err
	}
	return cloneThread(thread), nil
}

func (s *fileThreadStore) List(_ context.Context) ([]*aichat.Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	threads := make([]*aichat.Thread, 0, len(state.Threads))
	for _, thread := range state.Threads {
		threads = append(threads, cloneThread(thread))
	}
	sort.Slice(threads, func(i, j int) bool {
		return threads[i].UpdatedAt.After(threads[j].UpdatedAt)
	})
	return threads, nil
}

func (s *fileThreadStore) Get(_ context.Context, id string) (*aichat.Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	thread := findThread(state, id)
	if thread == nil {
		return nil, fmt.Errorf("thread %q not found", id)
	}
	return cloneThread(thread), nil
}

func (s *fileThreadStore) AppendMessage(_ context.Context, id string, msg aichat.UIMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.loadLocked()
	if err != nil {
		return err
	}
	thread := findThread(state, id)
	if thread == nil {
		return fmt.Errorf("thread %q not found", id)
	}
	thread.Messages = append(thread.Messages, msg)
	thread.UpdatedAt = time.Now()
	return s.saveLocked(state)
}

func (s *fileThreadStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.loadLocked()
	if err != nil {
		return err
	}
	for i, thread := range state.Threads {
		if thread.ID == id {
			state.Threads = append(state.Threads[:i], state.Threads[i+1:]...)
			return s.saveLocked(state)
		}
	}
	return fmt.Errorf("thread %q not found", id)
}

func (s *fileThreadStore) SetProviderSession(_ context.Context, id, providerSessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.loadLocked()
	if err != nil {
		return err
	}
	thread := findThread(state, id)
	if thread == nil {
		return fmt.Errorf("thread %q not found", id)
	}
	thread.ProviderSessionID = providerSessionID
	thread.UpdatedAt = time.Now()
	return s.saveLocked(state)
}

func (s *fileThreadStore) AddUsage(_ context.Context, id string, usage aichat.TurnUsage) (*aichat.Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	thread := findThread(state, id)
	if thread == nil {
		return nil, fmt.Errorf("thread %q not found", id)
	}
	thread.TotalInputTokens += usage.InputTokens
	thread.TotalOutputTokens += usage.OutputTokens
	thread.TotalCostUsd += usage.CostUSD
	thread.LastContextTokens = usage.InputTokens
	thread.UpdatedAt = time.Now()
	if err := s.saveLocked(state); err != nil {
		return nil, err
	}
	return cloneThread(thread), nil
}

func (s *fileThreadStore) loadLocked() (*threadStoreFile, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return &threadStoreFile{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return &threadStoreFile{}, nil
	}
	var state threadStoreFile
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("read chat threads %s: %w", s.path, err)
	}
	return &state, nil
}

func (s *fileThreadStore) saveLocked(state *threadStoreFile) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func findThread(state *threadStoreFile, id string) *aichat.Thread {
	for _, thread := range state.Threads {
		if thread.ID == id {
			return thread
		}
	}
	return nil
}

func cloneThread(thread *aichat.Thread) *aichat.Thread {
	if thread == nil {
		return nil
	}
	data, err := json.Marshal(thread)
	if err != nil {
		copy := *thread
		copy.Messages = append([]aichat.UIMessage(nil), thread.Messages...)
		return &copy
	}
	var out aichat.Thread
	if err := json.Unmarshal(data, &out); err != nil {
		copy := *thread
		copy.Messages = append([]aichat.UIMessage(nil), thread.Messages...)
		return &copy
	}
	return &out
}

func newThreadID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("thread-%d", time.Now().UnixNano())
	}
	return "thread-" + hex.EncodeToString(raw[:])
}

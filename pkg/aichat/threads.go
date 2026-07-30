package aichat

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type Thread struct {
	ID        string      `json:"id"`
	Title     string      `json:"title"`
	CreatedAt time.Time   `json:"createdAt"`
	UpdatedAt time.Time   `json:"updatedAt"`
	Messages  []UIMessage `json:"messages"`

	TotalInputTokens      int     `json:"totalInputTokens"`
	TotalOutputTokens     int     `json:"totalOutputTokens"`
	TotalReasoningTokens  int     `json:"totalReasoningTokens"`
	TotalCacheReadTokens  int     `json:"totalCacheReadTokens"`
	TotalCacheWriteTokens int     `json:"totalCacheWriteTokens"`
	TotalCostUSD          float64 `json:"totalCostUsd"`
	LastContextTokens     int     `json:"lastContextTokens"`
	ProviderSessionID     string  `json:"providerSessionId,omitempty"`
}

type TurnUsage struct {
	InputTokens      int
	OutputTokens     int
	ReasoningTokens  int
	CacheReadTokens  int
	CacheWriteTokens int
	CostUSD          float64
}

// ThreadStore is the persistence boundary for chat history, provider session
// identity, and cumulative usage. Implementations must be concurrency-safe.
type ThreadStore interface {
	Create(context.Context, string) (*Thread, error)
	List(context.Context) ([]*Thread, error)
	Get(context.Context, string) (*Thread, error)
	AppendMessage(context.Context, string, UIMessage) error
	ReplaceLastMessage(context.Context, string, UIMessage) error
	Delete(context.Context, string) error
	SetProviderSession(context.Context, string, string) error
	AddUsage(context.Context, string, TurnUsage) (*Thread, error)
}

type memoryThreadStore struct {
	mu      sync.Mutex
	seq     int
	threads map[string]*Thread
}

func NewMemoryThreadStore() ThreadStore {
	return &memoryThreadStore{threads: map[string]*Thread{}}
}

func (s *memoryThreadStore) Create(_ context.Context, title string) (*Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	now := time.Now()
	thread := &Thread{ID: fmt.Sprintf("thread-%d", s.seq), Title: title, CreatedAt: now, UpdatedAt: now, Messages: []UIMessage{}}
	s.threads[thread.ID] = thread
	return cloneThread(thread), nil
}

func (s *memoryThreadStore) List(context.Context) ([]*Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	threads := make([]*Thread, 0, len(s.threads))
	for _, thread := range s.threads {
		threads = append(threads, cloneThread(thread))
	}
	sort.Slice(threads, func(i, j int) bool { return threads[i].UpdatedAt.After(threads[j].UpdatedAt) })
	return threads, nil
}

func (s *memoryThreadStore) Get(_ context.Context, id string) (*Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	thread, ok := s.threads[id]
	if !ok {
		return nil, fmt.Errorf("thread %q not found", id)
	}
	return cloneThread(thread), nil
}

func (s *memoryThreadStore) AppendMessage(_ context.Context, id string, message UIMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	thread, err := s.thread(id)
	if err != nil {
		return err
	}
	thread.Messages = append(thread.Messages, message)
	thread.UpdatedAt = time.Now()
	return nil
}

func (s *memoryThreadStore) ReplaceLastMessage(_ context.Context, id string, message UIMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	thread, err := s.thread(id)
	if err != nil {
		return err
	}
	if err := validateLastMessageReplacement(thread.Messages, message); err != nil {
		return fmt.Errorf("thread %q: %w", id, err)
	}
	thread.Messages[len(thread.Messages)-1] = message
	thread.UpdatedAt = time.Now()
	return nil
}

func (s *memoryThreadStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.threads[id]; !ok {
		return fmt.Errorf("thread %q not found", id)
	}
	delete(s.threads, id)
	return nil
}

func (s *memoryThreadStore) SetProviderSession(_ context.Context, id, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	thread, err := s.thread(id)
	if err != nil {
		return err
	}
	thread.ProviderSessionID = sessionID
	thread.UpdatedAt = time.Now()
	return nil
}

func (s *memoryThreadStore) AddUsage(_ context.Context, id string, usage TurnUsage) (*Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	thread, err := s.thread(id)
	if err != nil {
		return nil, err
	}
	thread.TotalInputTokens += usage.InputTokens
	thread.TotalOutputTokens += usage.OutputTokens
	thread.TotalReasoningTokens += usage.ReasoningTokens
	thread.TotalCacheReadTokens += usage.CacheReadTokens
	thread.TotalCacheWriteTokens += usage.CacheWriteTokens
	thread.TotalCostUSD += usage.CostUSD
	thread.LastContextTokens = usage.InputTokens
	thread.UpdatedAt = time.Now()
	return cloneThread(thread), nil
}

func (s *memoryThreadStore) thread(id string) (*Thread, error) {
	thread, ok := s.threads[id]
	if !ok {
		return nil, fmt.Errorf("thread %q not found", id)
	}
	return thread, nil
}

func cloneThread(thread *Thread) *Thread {
	copy := *thread
	copy.Messages = append([]UIMessage(nil), thread.Messages...)
	return &copy
}

func validateLastMessageReplacement(messages []UIMessage, replacement UIMessage) error {
	if !strings.EqualFold(replacement.Role, "assistant") {
		return fmt.Errorf("replacement message must have assistant role")
	}
	if len(messages) == 0 {
		return fmt.Errorf("cannot replace a message in an empty thread")
	}
	if !strings.EqualFold(messages[len(messages)-1].Role, "assistant") {
		return fmt.Errorf("last stored message must have assistant role")
	}
	return nil
}

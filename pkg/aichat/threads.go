package aichat

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/session"
	"github.com/google/uuid"
)

const maxThreadSummaries = 100

var (
	ErrThreadNotFound        = errors.New("chat thread not found")
	ErrThreadRuntimeConflict = errors.New("chat thread runtime conflict")
	ErrForkSourceEmpty       = errors.New("chat thread has no conversation to fork")
)

type Thread struct {
	ID        string      `json:"id"`
	Title     string      `json:"title"`
	Revision  int64       `json:"revision"`
	CreatedAt time.Time   `json:"createdAt"`
	UpdatedAt time.Time   `json:"updatedAt"`
	Messages  []UIMessage `json:"messages,omitempty"`
	// Runtime is an identity, not an api.Model: the model's resolved Provider is
	// json:"-", so a client could not see which runtime the thread is locked to.
	// Projecting on the field rather than via a Thread-level marshaller keeps
	// Thread safe to embed — a promoted MarshalJSON silently drops the embedder's
	// own fields.
	Runtime    *api.RuntimeIdentity `json:"runtime,omitempty"`
	ForkedFrom string               `json:"forkedFrom,omitempty"`

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

// TitleSource records who named a conversation. It decides whether a later
// writer may rename it: a person's title outranks the agent's, which outranks
// one inferred from the opening message.
type TitleSource string

const (
	TitleSourceDerived TitleSource = "derived"
	TitleSourceAI      TitleSource = "ai"
	TitleSourceUser    TitleSource = "user"
)

type TitleUpdate struct {
	Title  string
	Source TitleSource
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
	SetRuntime(context.Context, string, api.Model) error
	Fork(context.Context, string) (*Thread, error)
	SetTitle(context.Context, string, TitleUpdate) error
	AddUsage(context.Context, string, TurnUsage) (*Thread, error)
}

type SessionReader interface {
	GetSession(context.Context, string) (*session.Session, error)
}

type memoryThreadStore struct {
	mu           sync.Mutex
	threads      map[string]*Thread
	titleSources map[string]TitleSource
}

func NewMemoryThreadStore() ThreadStore {
	return &memoryThreadStore{threads: map[string]*Thread{}, titleSources: map[string]TitleSource{}}
}

func (s *memoryThreadStore) Create(_ context.Context, title string) (*Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	thread := &Thread{ID: uuid.NewString(), Title: title, CreatedAt: now, UpdatedAt: now, Messages: []UIMessage{}}
	s.threads[thread.ID] = thread
	if strings.TrimSpace(title) != "" {
		s.titleSources[thread.ID] = TitleSourceUser
	}
	return cloneThread(thread), nil
}

func (s *memoryThreadStore) List(context.Context) ([]*Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	threads := make([]*Thread, 0, len(s.threads))
	for _, thread := range s.threads {
		summary := cloneThread(thread)
		summary.Messages = nil
		threads = append(threads, summary)
	}
	sort.Slice(threads, func(i, j int) bool { return threads[i].UpdatedAt.After(threads[j].UpdatedAt) })
	if len(threads) > maxThreadSummaries {
		threads = threads[:maxThreadSummaries]
	}
	return threads, nil
}

func (s *memoryThreadStore) Get(_ context.Context, id string) (*Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	thread, ok := s.threads[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrThreadNotFound, id)
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
	for _, existing := range thread.Messages {
		if existing.ID != "" && existing.ID == message.ID {
			if reflect.DeepEqual(existing, message) {
				return nil
			}
			return fmt.Errorf("thread %q message %q was replayed with different content", id, message.ID)
		}
	}
	thread.Messages = append(thread.Messages, message)
	touchMemoryThread(thread)
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
	touchMemoryThread(thread)
	return nil
}

func (s *memoryThreadStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.threads[id]; !ok {
		return fmt.Errorf("%w: %q", ErrThreadNotFound, id)
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
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("provider session ID cannot be empty")
	}
	if thread.ProviderSessionID != "" && thread.ProviderSessionID != sessionID {
		return fmt.Errorf(
			"provider session is already bound to %q, cannot replace it with %q",
			thread.ProviderSessionID,
			sessionID,
		)
	}
	if thread.ProviderSessionID == sessionID {
		return nil
	}
	thread.ProviderSessionID = sessionID
	touchMemoryThread(thread)
	return nil
}

func (s *memoryThreadStore) SetRuntime(_ context.Context, id string, runtime api.Model) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	thread, err := s.thread(id)
	if err != nil {
		return err
	}
	identity, err := threadRuntimeIdentity(runtime)
	if err != nil {
		return err
	}
	if thread.Runtime != nil {
		if sameThreadRuntime(*thread.Runtime, identity) {
			return nil
		}
		return fmt.Errorf("%w: runtime is already bound to %s/%s", ErrThreadRuntimeConflict,
			thread.Runtime.Runtime(), thread.Runtime.Model)
	}
	thread.Runtime = &identity
	touchMemoryThread(thread)
	return nil
}

func (s *memoryThreadStore) Fork(_ context.Context, id string) (*Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	source, err := s.thread(id)
	if err != nil {
		return nil, err
	}
	title, seed, err := forkSeedMessage(source)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	fork := &Thread{
		ID: uuid.NewString(), Title: title, CreatedAt: now, UpdatedAt: now,
		Messages: []UIMessage{seed}, ForkedFrom: source.ID,
	}
	s.threads[fork.ID] = fork
	s.titleSources[fork.ID] = TitleSourceDerived
	return cloneThread(fork), nil
}

func (s *memoryThreadStore) SetTitle(_ context.Context, id string, update TitleUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	thread, err := s.thread(id)
	if err != nil {
		return err
	}
	title, err := normalizeTitle(update)
	if err != nil {
		return err
	}
	if !titleWins(thread.Title, s.titleSources[id], update.Source) {
		return nil
	}
	thread.Title = title
	s.titleSources[id] = update.Source
	touchMemoryThread(thread)
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
	touchMemoryThread(thread)
	return cloneThread(thread), nil
}

func (s *memoryThreadStore) thread(id string) (*Thread, error) {
	thread, ok := s.threads[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrThreadNotFound, id)
	}
	return thread, nil
}

func cloneThread(thread *Thread) *Thread {
	copy := *thread
	copy.Messages = append([]UIMessage(nil), thread.Messages...)
	if thread.Runtime != nil {
		runtime := *thread.Runtime
		copy.Runtime = &runtime
	}
	return &copy
}

func touchMemoryThread(thread *Thread) {
	thread.Revision++
	thread.UpdatedAt = time.Now()
}

// threadRuntimeIdentity projects a resolved model onto the thread's stored
// identity. It goes through RuntimeIdentityOf rather than hand-building the
// struct: a hand-built identity omitted Mode, so a client that read the thread
// back and posted it as a request named a runtime the thread was never locked
// to — the whole reason an agent-backed session came back as "api".
func threadRuntimeIdentity(runtime api.Model) (api.RuntimeIdentity, error) {
	resolved, err := ai.Resolve(runtime)
	if err != nil {
		return api.RuntimeIdentity{}, fmt.Errorf("resolve thread runtime: %w", err)
	}
	identity := api.RuntimeIdentityOf(resolved)
	identity.Model = strings.TrimSpace(identity.Model)
	// Effort is a per-turn choice, not part of the thread's locked identity —
	// sameThreadRuntime compares model and (provider, mode) only. Storing it
	// anyway made the write-once metadata lock stricter than the check above it,
	// so raising the effort on a later turn read as a runtime change and the
	// thread refused it.
	identity.Effort = ""
	if identity.Model == "" || !identity.Runtime().Valid() {
		return api.RuntimeIdentity{}, fmt.Errorf("thread runtime requires a model name and a valid (provider, mode) pair")
	}
	return identity, nil
}

func sameThreadRuntime(left, right api.RuntimeIdentity) bool {
	return left.Model == right.Model && left.Runtime() == right.Runtime()
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
	if messages[len(messages)-1].ID != "" && replacement.ID != messages[len(messages)-1].ID {
		return fmt.Errorf(
			"replacement message ID %q does not match stored message %q",
			replacement.ID,
			messages[len(messages)-1].ID,
		)
	}
	return nil
}

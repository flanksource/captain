package cli

import "sync"

type chatBroker struct {
	mu        sync.Mutex
	byRun     map[string]*chatSession
	bySession map[string]*chatSession
}

func newChatBroker() *chatBroker {
	return &chatBroker{byRun: map[string]*chatSession{}, bySession: map[string]*chatSession{}}
}

var promptChats = newChatBroker()

func (b *chatBroker) register(chat *chatSession) {
	b.mu.Lock()
	b.byRun[chat.runID] = chat
	b.mu.Unlock()
}

func (b *chatBroker) bindSession(chat *chatSession, sessionID string) {
	if sessionID == "" {
		return
	}
	b.mu.Lock()
	b.bySession[sessionID] = chat
	b.mu.Unlock()
}

func (b *chatBroker) getRun(runID string) (*chatSession, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	chat, ok := b.byRun[runID]
	return chat, ok
}

func (b *chatBroker) getSession(sessionID string) (*chatSession, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	chat, ok := b.bySession[sessionID]
	if ok && chat.terminalState() {
		delete(b.bySession, sessionID)
		return nil, false
	}
	return chat, ok
}

func (b *chatBroker) finish(chat *chatSession) {
	sessionID := chat.sessionID()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.bySession[sessionID] == chat {
		delete(b.bySession, sessionID)
	}
}

func (b *chatBroker) stopAll() {
	b.mu.Lock()
	chats := make([]*chatSession, 0, len(b.byRun))
	for _, chat := range b.byRun {
		chats = append(chats, chat)
	}
	b.mu.Unlock()
	for _, chat := range chats {
		chat.stop()
	}
}

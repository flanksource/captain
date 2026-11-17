package cli

import (
	"context"
	"fmt"
	"sync"

	"github.com/flanksource/captain/pkg/ai"
)

// sessionRunQueue admits one turn at a time into a shared provider session.
// Every turn keeps its own tools, permissions, callbacks and outcome. A fresh
// request replaces the conversation for subsequent turns without interrupting
// turns already delivered to the previous conversation.
type sessionRunQueue struct {
	mu    sync.Mutex
	tails map[string]*promptConversation
}

type promptConversation struct{ tail *runStream }

var promptSessionQueue = sessionRunQueue{tails: map[string]*promptConversation{}}

func (q *sessionRunQueue) enqueue(launch promptLaunch, stream *runStream) *runStream {
	key := launch.SessionKey
	if key == "" && launch.Rendered.Input.SessionID != "" {
		key = launch.Rendered.Provider + "/" + launch.Rendered.Mode + "/" + launch.Rendered.Input.SessionID
	}
	if key == "" {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	conversation := q.tails[key]
	if conversation == nil && launch.Rendered.Input.SessionID != "" {
		conversation = q.tails[launch.Rendered.Provider+"/"+launch.Rendered.Mode+"/"+launch.Rendered.Input.SessionID]
	}
	if conversation == nil || launch.Fresh {
		conversation = &promptConversation{}
	}
	previous := conversation.tail
	stream.previous = previous
	conversation.tail, stream.conversation = stream, conversation
	q.tails[key] = conversation
	return previous
}

func continuePromptSession(ctx context.Context, previous *runStream, rendered *PromptRenderResult) error {
	if previous == nil {
		return nil
	}
	var cause string
	for previous != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-previous.finished:
		}
		previous.mu.Lock()
		id := previous.resumeSession
		cause = previous.errMsg
		previous.mu.Unlock()
		if id != "" {
			rendered.Input.SessionID, rendered.Config.SessionID = id, id
			return nil
		}
		previous = previous.previous
	}
	return fmt.Errorf("shared session did not start: %s", cause)
}

// observeSession also registers the provider identity, so an explicit resume
// joins the conversation already running under a host's SessionKey.
func (q *sessionRunQueue) observeSession(stream *runStream, rendered PromptRenderResult, event ai.Event) {
	if event.SessionID == "" {
		return
	}
	stream.mu.Lock()
	stream.resumeSession = event.SessionID
	stream.mu.Unlock()
	key := rendered.Provider + "/" + rendered.Mode + "/" + event.SessionID
	q.mu.Lock()
	defer q.mu.Unlock()
	if stream.conversation != nil {
		q.tails[key] = stream.conversation
	} else if _, exists := q.tails[key]; !exists {
		stream.conversation = &promptConversation{tail: stream}
		q.tails[key] = stream.conversation
	}
}

func (q *sessionRunQueue) prune() {
	q.mu.Lock()
	defer q.mu.Unlock()
	for key, conversation := range q.tails {
		finished := true
		for stream := conversation.tail; stream != nil; stream = stream.previous {
			if done, _ := stream.terminalAt(); !done {
				finished = false
				break
			}
		}
		if finished {
			delete(q.tails, key)
		}
	}
}

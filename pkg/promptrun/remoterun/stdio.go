// The channel between a relocated run and its caller over the run's process: messages go out on
// its stdout as JSON, and the caller's replies come back on its stdin, matched to calls by ID.
package remoterun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"sync"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/promptrun"
)

// Stdio is the environment's side of the channel: it sends messages on out, the process's
// stdout, and reads the caller's replies from in, its stdin.
func Stdio(out io.Writer, in io.Reader) Serve {
	s := &stdio{out: json.NewEncoder(out), pending: map[string]chan Reply{}}
	go s.read(json.NewDecoder(in))
	return Serve{Send: s.send, Call: s.call}
}

type stdio struct {
	sending sync.Mutex
	out     *json.Encoder

	mu      sync.Mutex
	pending map[string]chan Reply
	calls   int
	stopped error
}

func (s *stdio) send(message Message) error {
	s.sending.Lock()
	defer s.sending.Unlock()
	return s.out.Encode(message)
}

// call sends message as a call and waits for its reply.
func (s *stdio) call(message Message) (Reply, error) {
	s.mu.Lock()
	if s.stopped != nil {
		defer s.mu.Unlock()
		return Reply{}, s.stopped
	}
	s.calls++
	message.ID = "call-" + strconv.Itoa(s.calls)
	reply := make(chan Reply, 1)
	s.pending[message.ID] = reply
	s.mu.Unlock()
	if err := s.send(message); err != nil {
		s.mu.Lock()
		delete(s.pending, message.ID)
		s.mu.Unlock()
		return Reply{}, err
	}
	answer, ok := <-reply
	if !ok {
		s.mu.Lock()
		defer s.mu.Unlock()
		return Reply{}, s.stopped
	}
	return answer, nil
}

// read hands each reply to its call until in ends, then fails the calls still waiting.
func (s *stdio) read(in *json.Decoder) {
	for {
		var reply Reply
		if err := in.Decode(&reply); err != nil {
			s.mu.Lock()
			s.stopped = fmt.Errorf("the caller stopped answering: %w", err)
			for id, waiting := range s.pending {
				close(waiting)
				delete(s.pending, id)
			}
			s.mu.Unlock()
			return
		}
		s.mu.Lock()
		waiting, ok := s.pending[reply.ID]
		delete(s.pending, reply.ID)
		s.mu.Unlock()
		if ok {
			waiting <- reply
		}
	}
}

// Relay is the caller's side of the channel. It reads the run's messages from out, its process's
// stdout; calls started with the spec the run starts with; hands events and verification progress
// to in's OnEvent and Verify.Progress; answers tool calls and approvals with in's tools and
// OnApproval on input, its stdin; and returns the run's result and error. Handlers get ctx. Relay
// returns when the run sends its result, out ends, or started fails, so a caller cancels a run by
// ending its process.
func Relay(ctx context.Context, in promptrun.Input, started func(api.Spec) error, out io.Reader, input io.Writer) (promptrun.Result, error) {
	messages := json.NewDecoder(out)
	replies := &replier{out: json.NewEncoder(input)}
	var calls sync.WaitGroup
	defer calls.Wait()
	for {
		var message Message
		if err := messages.Decode(&message); err != nil {
			if errors.Is(err, io.EOF) {
				return promptrun.Result{}, errors.New("the relocated run ended without a result")
			}
			return promptrun.Result{}, fmt.Errorf("read the relocated run's messages: %w", err)
		}
		switch message.Kind {
		case KindStarted:
			var spec api.Spec
			if message.Spec != nil {
				spec = *message.Spec
			}
			if err := started(spec); err != nil {
				return promptrun.Result{}, fmt.Errorf("the relocated run started, but its start couldn't be recorded: %w", err)
			}
		case KindEvent:
			if in.OnEvent != nil {
				in.OnEvent(message.Iteration, message.DecodedEvent())
			}
		case KindProgress:
			if in.Verify.Progress != nil && message.Progress != nil {
				in.Verify.Progress(*message.Progress)
			}
		case KindTool, KindApproval:
			calls.Add(1)
			go func() {
				defer calls.Done()
				replies.send(answer(ctx, in, message))
			}()
		case KindResult:
			result := message.Result.Unwrap()
			if message.Error != "" {
				return result, errors.New(message.Error)
			}
			return result, nil
		default:
			return promptrun.Result{}, fmt.Errorf("the relocated run sent a message of unknown kind %q", message.Kind)
		}
	}
}

// answer is the caller's reply to call: its tool's output, or its approval decision.
func answer(ctx context.Context, in promptrun.Input, call Message) Reply {
	reply := Reply{ID: call.ID}
	if call.Kind == KindApproval {
		if in.Config.OnApproval == nil || call.Approval == nil {
			reply.Error = "the caller asks no approvals"
			return reply
		}
		decision, err := in.Config.OnApproval(ctx, *call.Approval)
		if err != nil {
			reply.Error = err.Error()
			return reply
		}
		reply.Decision = &decision
		return reply
	}
	i := slices.IndexFunc(in.Config.Tools, func(tool api.ToolDefinition) bool { return tool.Name == call.Tool && tool.Handler != nil })
	if i < 0 {
		reply.Error = fmt.Sprintf("the caller has no tool %q", call.Tool)
		return reply
	}
	output, err := in.Config.Tools[i].Handler(ctx, call.Input)
	if err != nil {
		reply.Error = err.Error()
		return reply
	}
	if reply.Output, err = json.Marshal(output); err != nil {
		reply.Error = fmt.Sprintf("encode the output of tool %s: %v", call.Tool, err)
	}
	return reply
}

// replier writes replies one at a time. A reply the run can't read is dropped: the run has ended,
// and Relay sees its output end.
type replier struct {
	mu  sync.Mutex
	out *json.Encoder
}

func (r *replier) send(reply Reply) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.out.Encode(reply)
}

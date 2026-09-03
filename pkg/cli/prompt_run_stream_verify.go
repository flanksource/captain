package cli

import "github.com/flanksource/captain/pkg/api"

// VerifyFrame is the run's current verification state: the newest report a
// verifier produced, and whether it is the verdict or a snapshot of a check
// still running. Only the latest is kept — a superseded progress snapshot has
// no reader — so a late subscriber gets the current state of the check on
// connect the same way it gets the current `run` and `state`.
type VerifyFrame struct {
	Report *api.VerifyReport `json:"report"`
	Done   bool              `json:"done"`
}

// setVerify replaces the run's verification state and publishes it as its own
// SSE event. It is deliberately not a transcript frame: a check reporting every
// few hundred milliseconds would otherwise append a message per snapshot to a
// buffer that is replayed in full to every later subscriber, so the transcript
// would grow with superseded counts and the verdict would be buried in them.
//
// It stops at done like publish does. A finished run's subscribers are already
// closed and its snapshot is the terminal one a late reader gets; a check still
// reporting after that would rewrite the state of a run that has ended.
func (s *runStream) setVerify(frame VerifyFrame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	s.verify = &frame
	s.publishEventLocked(runStreamEvent{name: "verify", data: frame})
}

func cloneVerify(frame *VerifyFrame) *VerifyFrame {
	if frame == nil {
		return nil
	}
	clone := *frame
	return &clone
}

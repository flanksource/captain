// Package observation records bounded, provider-boundary evidence for the
// captain.observation/v1 contract. A recorder is carried in context so ordinary
// provider interfaces and captain prompt run remain unchanged.
package observation

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"unicode"

	"github.com/flanksource/captain/pkg/api"
)

const maxEvents = 256

type recorderContextKey struct{}

type effortSample struct {
	id         string
	state      api.ObservationFactState
	value      string
	reasonCode string
}

type Snapshot struct {
	Dispatch        []api.ObservationDispatchEvent
	Permissions     []api.ObservationPermissionEvent
	Tools           []api.ObservationToolEvent
	Effort          api.ObservationStringFact
	Usage           *api.Usage
	UsageObserved   bool
	DispatchPartial bool
	Overflow        bool
}

type Recorder struct {
	mu sync.Mutex

	dispatch    []api.ObservationDispatchEvent
	permissions []api.ObservationPermissionEvent
	tools       []api.ObservationToolEvent
	efforts     []effortSample
	usage       *api.Usage
	usageSeen   bool
	toolIndex   map[string]int
	deniedTools map[string]bool
	overflow    bool
}

// NewRecorder creates an empty, concurrency-safe observation recorder.
func NewRecorder() *Recorder {
	return &Recorder{
		toolIndex:   map[string]int{},
		deniedTools: map[string]bool{},
	}
}

// ContextWithRecorder attaches recorder to provider execution without changing
// the provider interface.
func ContextWithRecorder(ctx context.Context, recorder *Recorder) context.Context {
	if recorder == nil {
		return ctx
	}
	return context.WithValue(ctx, recorderContextKey{}, recorder)
}

// FromContext returns the attached recorder, or nil outside observation mode.
func FromContext(ctx context.Context) *Recorder {
	if ctx == nil {
		return nil
	}
	recorder, _ := ctx.Value(recorderContextKey{}).(*Recorder)
	return recorder
}

// RecordReasoningDispatch records only the normalized reasoning control and a
// fixed provider boundary. Native requests, argv, prompts, and credentials are
// deliberately never retained.
func RecordReasoningDispatch(ctx context.Context, boundary string, present bool, value string) {
	if recorder := FromContext(ctx); recorder != nil {
		state := api.ObservationFactUnset
		if present {
			state = api.ObservationFactKnown
		}
		recorder.recordReasoningDispatch(boundary, state, value, "")
	}
}

// RecordReasoningDispatchUnknown records that a provider-native dispatch
// occurred but its reasoning control could not be inspected safely.
func RecordReasoningDispatchUnknown(ctx context.Context, boundary, reasonCode string) {
	if recorder := FromContext(ctx); recorder != nil {
		recorder.recordReasoningDispatch(boundary, api.ObservationFactUnknown, "", reasonCode)
	}
}

// RecordUsage records the latest provider or terminal-event usage sample. Nil
// clears an earlier sample; a non-nil all-zero value remains explicitly known.
func RecordUsage(ctx context.Context, usage *api.Usage) {
	if recorder := FromContext(ctx); recorder != nil {
		recorder.recordUsage(usage)
	}
}

func (r *Recorder) recordUsage(usage *api.Usage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.usageSeen = true
	if usage == nil {
		r.usage = nil
		return
	}
	usageCopy := *usage
	r.usage = &usageCopy
}

func (r *Recorder) recordReasoningDispatch(boundary string, state api.ObservationFactState, value, reasonCode string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.dispatch) >= maxEvents {
		r.overflow = true
		return
	}
	id := fmt.Sprintf("dispatch-%d", len(r.dispatch)+1)
	r.dispatch = append(r.dispatch, api.ObservationDispatchEvent{
		ID: id, Attempt: len(r.dispatch) + 1, Boundary: safeName(boundary),
	})
	r.efforts = append(r.efforts, effortSample{
		id: id, state: state, value: safeName(value), reasonCode: safeName(reasonCode),
	})
}

// PermissionBroker wraps the runtime permission authority and records its
// returned decision. Observation mode defaults to deny when no authority was
// supplied, preventing an unattended machine-oriented run from auto-approving
// side effects.
func (r *Recorder) PermissionBroker(next api.PermissionFunc) api.PermissionFunc {
	return func(ctx context.Context, request api.PermissionRequest) (api.PermissionDecision, error) {
		var decision api.PermissionDecision
		var err error
		if next == nil {
			decision = api.PermissionDecision{Allow: false, Message: "denied by captain observation broker"}
		} else {
			decision, err = next(ctx, request)
		}
		r.recordPermission(request, decision.Allow && err == nil)
		return decision, err
	}
}

func (r *Recorder) recordPermission(request api.PermissionRequest, allowed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	toolCallID := safeName(request.ToolUseID)
	tool := safeName(request.Tool)
	if len(r.permissions) >= maxEvents {
		r.overflow = true
		return
	}
	decision := "denied"
	if allowed {
		decision = "allowed"
	}
	r.permissions = append(r.permissions, api.ObservationPermissionEvent{
		ID:         fmt.Sprintf("permission-%d", len(r.permissions)+1),
		ToolCallID: toolCallID, Tool: tool, Decision: decision, DecidedBy: "captain_broker",
	})
	index := r.ensureTool(toolCallID, tool)
	if !allowed && index >= 0 {
		r.deniedTools[toolKey(toolCallID, tool)] = true
		r.tools[index].Execution.State = "not_started"
	}
}

// RecordEvent folds provider events into a content-free tool lifecycle. Tool
// inputs and outputs are intentionally discarded rather than redacted after the
// fact, so secrets cannot enter the observation recorder.
func (r *Recorder) RecordEvent(event api.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	toolCallID := safeName(event.ToolCallID)
	tool := safeName(event.Tool)
	switch event.Kind {
	case api.EventToolUse:
		r.ensureTool(toolCallID, tool)
	case api.EventToolResult:
		index := r.ensureTool(toolCallID, tool)
		if index < 0 {
			return
		}
		if r.deniedTools[toolKey(toolCallID, r.tools[index].Name)] {
			r.tools[index].Execution.State = "not_started"
		} else if event.Success {
			r.tools[index].Execution.State = "completed"
		} else {
			r.tools[index].Execution.State = "failed"
		}
	}
}

func (r *Recorder) ensureTool(toolCallID, tool string) int {
	key := toolKey(toolCallID, tool)
	if toolCallID != "" {
		if index, ok := r.toolIndex[key]; ok {
			if r.tools[index].Name == "" && tool != "" {
				r.tools[index].Name = tool
			}
			return index
		}
	}
	if len(r.tools) >= maxEvents {
		r.overflow = true
		return -1
	}
	index := len(r.tools)
	r.tools = append(r.tools, api.ObservationToolEvent{
		ID: fmt.Sprintf("tool-%d", index+1), ToolCallID: toolCallID, Name: tool,
		Execution: api.ObservationToolExecution{State: "started"},
	})
	if toolCallID != "" {
		r.toolIndex[key] = index
	}
	return index
}

func toolKey(toolCallID, tool string) string {
	if toolCallID != "" {
		return "id:" + toolCallID
	}
	return "tool:" + tool
}

// Snapshot returns a detached copy of all normalized recorder evidence.
func (r *Recorder) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	var usage *api.Usage
	if r.usage != nil {
		usageCopy := *r.usage
		usage = &usageCopy
	}
	dispatchPartial := false
	for _, sample := range r.efforts {
		if sample.state == api.ObservationFactUnknown {
			dispatchPartial = true
			break
		}
	}
	return Snapshot{
		Dispatch:        append([]api.ObservationDispatchEvent(nil), r.dispatch...),
		Permissions:     append([]api.ObservationPermissionEvent(nil), r.permissions...),
		Tools:           append([]api.ObservationToolEvent(nil), r.tools...),
		Effort:          observedEffort(r.efforts, r.overflow),
		Usage:           usage,
		UsageObserved:   r.usageSeen,
		DispatchPartial: dispatchPartial,
		Overflow:        r.overflow,
	}
}

func observedEffort(samples []effortSample, overflow bool) api.ObservationStringFact {
	if overflow {
		return api.ObservationStringFact{State: api.ObservationFactUnknown, ReasonCode: "capture_truncated"}
	}
	if len(samples) == 0 {
		return api.ObservationStringFact{State: api.ObservationFactUnknown, ReasonCode: "dispatch_not_observed"}
	}
	refs := make([]string, len(samples))
	for i := range samples {
		refs[i] = samples[i].id
		if samples[i].state == api.ObservationFactUnknown {
			return api.ObservationStringFact{
				State: api.ObservationFactUnknown, ReasonCode: samples[i].reasonCode, EvidenceRefs: refs[:i+1],
			}
		}
	}
	first := samples[0]
	for _, sample := range samples[1:] {
		if sample.state != first.state || sample.value != first.value {
			return api.ObservationStringFact{
				State: api.ObservationFactUnknown, ReasonCode: "inconsistent_dispatch_values", EvidenceRefs: refs,
			}
		}
	}
	if first.state == api.ObservationFactUnset {
		return api.ObservationStringFact{State: api.ObservationFactUnset, EvidenceRefs: refs}
	}
	value := first.value
	return api.ObservationStringFact{State: api.ObservationFactKnown, Value: &value, EvidenceRefs: refs}
}

// safeName keeps recorder strings bounded and token-shaped. Provider payloads
// never pass through this function; it is reserved for boundary, tool, and
// control identifiers whose useful alphabet is deliberately small.
func safeName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var out strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._:/-", r) {
			out.WriteRune(r)
		} else {
			out.WriteByte('_')
		}
		if out.Len() >= 128 {
			break
		}
	}
	return out.String()
}

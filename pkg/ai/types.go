package ai

import (
	"github.com/flanksource/captain/pkg/api"
)

// Request is one model/agent call. It is a type alias for the serializable
// api.Spec (model, prompt, budget, memory, permissions, setup, session) —
// ai.Request IS the spec, so providers read the nested fields directly:
// req.Temperature/req.Effort (Model is inlined), req.Prompt.User,
// req.Permissions.Mode, req.Cwd(), req.Memory.Skills. The structured-output Go
// type rides on Prompt.Schema; the
// runtime-only tool-permission broker callback lives on Config.CanUseTool.
type Request = api.Spec

// The tool-permission broker, the buffered Response, the streaming Event/EventKind
// and the provider construction Config now live in pkg/api (the stable runtime
// contract). They are re-exported here as aliases so existing call sites and
// clicky/aichat's captainai.* keep compiling unchanged.
type (
	PermissionFunc      = api.PermissionFunc
	PermissionRequest   = api.PermissionRequest
	PermissionDecision  = api.PermissionDecision
	Response            = api.Response
	TerminalOutcome     = api.TerminalOutcome
	TerminalOutcomeKind = api.TerminalOutcomeKind
	TerminalPlan        = api.TerminalPlan
	TerminalQuestion    = api.TerminalQuestion
	EventKind           = api.EventKind
	Event               = api.Event
	Config              = api.Config
)

const (
	TerminalOutcomePlan      = api.TerminalOutcomePlan
	TerminalOutcomeQuestions = api.TerminalOutcomeQuestions
)

const (
	EventText       = api.EventText
	EventThinking   = api.EventThinking
	EventToolUse    = api.EventToolUse
	EventToolResult = api.EventToolResult
	EventResult     = api.EventResult
	EventError      = api.EventError
	EventSystem     = api.EventSystem
	EventPermission = api.EventPermission
)

// Usage is an alias for the canonical api.Usage (per-call token breakdown).
type Usage = api.Usage

// NetInputTokens / NetOutputTokens re-export the disjoint-bucket normalizers so
// provider packages (which import ai, not api) can enforce the Usage invariant
// at their parse boundary.
var (
	NetInputTokens  = api.NetInputTokens
	NetOutputTokens = api.NetOutputTokens
)

// Cost and Costs are aliases for the canonical api types (token + money
// accounting). The methods (Total/Add/Sum/ByModel) live on the api types.
type Cost = api.Cost
type Costs = api.Costs

package api

// COMPAT(unified-approval): everything in this file is removed in Phase 6 of
// docs/plans/unified-approval-callback.md, once no Flanksource module uses the
// deprecated names and no deprecation warning has been logged for a release.

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"

	"github.com/flanksource/commons/logger"
)

// PermissionFunc is the pre-rename name of ApprovalFunc.
//
// Deprecated: use ApprovalFunc. Removed in the unified-approval Phase 6.
type PermissionFunc = ApprovalFunc

// PermissionRequest is the pre-rename name of ApprovalRequest.
//
// Deprecated: use ApprovalRequest. Removed in the unified-approval Phase 6.
type PermissionRequest = ApprovalRequest

// PermissionDecision is the pre-rename name of ApprovalDecision.
//
// Deprecated: use ApprovalDecision. Removed in the unified-approval Phase 6.
type PermissionDecision = ApprovalDecision

// ErrLegacyApprovalSkipped is returned, without calling the callback, for a
// request the pre-rename CanUseTool contract never delivered. A provider that
// sees it answers the request exactly as it would with no callback attached.
var ErrLegacyApprovalSkipped = errors.New("not sent to a deprecated CanUseTool callback")

// ApprovalBinding is the approval callback a provider should use. Legacy marks a
// callback registered through a deprecated entry point, which must keep the
// provider's pre-rename behaviour for request kinds it never received.
type ApprovalBinding struct {
	Func   ApprovalFunc
	Legacy bool
}

// Approvals resolves the approval callback from OnApproval or the deprecated
// CanUseTool. Setting both is a conflict, not a precedence question.
func (c Config) Approvals() (ApprovalBinding, error) {
	switch {
	case c.OnApproval != nil && c.CanUseTool != nil:
		return ApprovalBinding{}, fmt.Errorf("config sets both OnApproval and the deprecated CanUseTool; set only OnApproval")
	case c.OnApproval != nil:
		return ApprovalBinding{Func: c.OnApproval}, nil
	case c.CanUseTool != nil:
		return ApprovalBinding{Func: LegacyApprovalFunc(c.CanUseTool, "Config.CanUseTool", ""), Legacy: true}, nil
	}
	return ApprovalBinding{}, nil
}

// WrapApprovals decorates whichever approval callback is set. The deprecated
// CanUseTool is wrapped in place rather than moved, so legacy routing is still
// applied at provider construction, outside the decoration.
func (c *Config) WrapApprovals(wrap func(ApprovalFunc) ApprovalFunc) {
	if c.OnApproval != nil {
		c.OnApproval = wrap(c.OnApproval)
	}
	if c.CanUseTool != nil {
		c.CanUseTool = wrap(c.CanUseTool)
	}
}

// resolveApprovals folds the deprecated CanUseTool into OnApproval once, at
// provider construction, so providers only ever read OnApproval.
func (c *Config) resolveApprovals() error {
	binding, err := c.Approvals()
	if err != nil {
		return err
	}
	c.OnApproval, c.CanUseTool, c.legacyApprovals = binding.Func, nil, binding.Legacy
	return nil
}

// LegacyApprovals reports that OnApproval came from the deprecated CanUseTool
// and runs in legacy mode, so a provider keeps its pre-rename behaviour for the
// request kinds that callback never received.
func (c Config) LegacyApprovals() bool { return c.legacyApprovals }

// LegacyApprovalFunc runs fn in legacy mode: it forwards only requests the
// pre-rename contract delivered (LegacyContract) and refuses every other one
// with ErrLegacyApprovalSkipped. entryPoint names the deprecated API, and
// location, when known, is the caller's file:line.
func LegacyApprovalFunc(fn ApprovalFunc, entryPoint, location string) ApprovalFunc {
	WarnDeprecatedEntryPoint(entryPoint, location)
	return func(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error) {
		if req.LegacyContract {
			return fn(ctx, req)
		}
		return ApprovalDecision{}, fmt.Errorf("%s request (%s, %s) %w", req.Kind, req.Tool, req.ToolUseID, ErrLegacyApprovalSkipped)
	}
}

// LogLegacyApprovalSkip records that a provider answered a request natively
// because the callback runs in legacy mode: a warning the first time a run skips
// a kind, debug after that.
func LogLegacyApprovalSkip(run string, kind ApprovalKind, skipped error, answer string) {
	key := run + "\x00" + string(kind)
	if _, seen := compat.skips.LoadOrStore(key, struct{}{}); seen {
		compat.debugf("captain: %v; answered natively with %s", skipped, answer)
		return
	}
	compat.warnf("captain: %v; answered natively with %s. Switch to Config.OnApproval to handle it.", skipped, answer)
}

var compatLogger = logger.GetLogger("captain")

var compat = struct {
	warnf, debugf func(string, ...any)
	entryPoints   *sync.Map
	skips         *sync.Map
}{
	warnf:       func(format string, args ...any) { compatLogger.Warnf(format, args...) },
	debugf:      func(format string, args ...any) { compatLogger.Debugf(format, args...) },
	entryPoints: &sync.Map{},
	skips:       &sync.Map{},
}

// CallerLocation returns the file:line skip frames above its caller, for naming
// the code that called a deprecated method.
func CallerLocation(skip int) string {
	_, file, line, ok := runtime.Caller(skip + 1)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s:%d", file, line)
}

// WarnDeprecatedEntryPoint logs, once per process, that a deprecated approval
// entry point is in use and what legacy mode withholds from it.
func WarnDeprecatedEntryPoint(entryPoint, location string) {
	if _, seen := compat.entryPoints.LoadOrStore(entryPoint, struct{}{}); seen {
		return
	}
	replacement, ok := deprecatedEntryPoints[entryPoint]
	if !ok {
		panic(fmt.Sprintf("unknown deprecated approval entry point %q", entryPoint))
	}
	message := fmt.Sprintf("captain: %s is deprecated; use %s (removed in unified-approval Phase 6)", entryPoint, replacement.name)
	if location != "" {
		message += " (called from " + location + ")"
	}
	if replacement.legacyMode {
		message += ". Legacy mode: Codex command, file-change and permission approvals, and elicitations, are answered natively and are not sent to this callback."
	}
	compat.warnf("%s", message)
}

// deprecatedEntryPoints maps each deprecated entry point to its replacement.
// legacyMode marks the ones that register a callback, and so run it in legacy
// mode; Broker.CanUseTool is the callee, not a callback registration.
var deprecatedEntryPoints = map[string]struct {
	name       string
	legacyMode bool
}{
	"Config.CanUseTool":              {name: "Config.OnApproval", legacyMode: true},
	"callertools.Options.CanUseTool": {name: "callertools.Options.OnApproval", legacyMode: true},
	"Recorder.PermissionBroker":      {name: "Recorder.ApprovalBroker", legacyMode: true},
	"Broker.CanUseTool":              {name: "Broker.OnApproval"},
}

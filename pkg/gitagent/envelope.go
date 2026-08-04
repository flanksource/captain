// The control envelope rides on push options (§4): option 0 is the version
// tag, the remainder are key=value pairs. Envelope values are never read from
// commit trailers — trailers are heuristically extracted and forgeable (R4.2).
package gitagent

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	// EnvelopeVersionTag is push option 0 for protocol v1.
	EnvelopeVersionTag = "captain-envelope-v1"

	// ProtocolVersion is the version this implementation speaks.
	ProtocolVersion = 1

	// MaxHookDepth bounds hook-recursion depth (R5.4/H15). Depth 0 is the top
	// level; a prompt hook increments it per nesting level.
	MaxHookDepth = 4

	// maxPushOptions bounds how many options a decoder will look at.
	maxPushOptions = 16
)

// RelayMode selects how the sidecar reports the supervisor's verdict (§6.4).
type RelayMode string

const (
	// RelaySync blocks the agent's push until the supervisor has decided. It
	// is the default and MUST be supported (R6.8).
	RelaySync RelayMode = "sync"
	// RelayAsync accepts after hook set #1 and reports out-of-band. It MUST
	// NOT be the default (R6.8).
	RelayAsync RelayMode = "async"
)

// oidRe accepts SHA-1 or SHA-256 object names.
var oidRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// ValidateOID checks that s is a full hex object id.
func ValidateOID(s string) error {
	if !oidRe.MatchString(s) {
		return fmt.Errorf("%q is not a full object id", s)
	}
	return nil
}

// Envelope is the §4 control envelope.
type Envelope struct {
	Version int       `json:"v"`
	Task    string    `json:"task"`
	Attempt int       `json:"attempt"`
	Base    string    `json:"base"`            // supervisor HEAD OID at dispatch (R10.1)
	Depth   int       `json:"depth"`           // hook-recursion depth, 0 at top level
	Agent   string    `json:"agent,omitempty"` // dispatch only: target agent
	Relay   RelayMode `json:"relay,omitempty"` // dispatch only
}

// Validate checks every field an envelope always carries. Agent and Relay are
// dispatch-only and validated when present.
func (e Envelope) Validate() error {
	if e.Version != ProtocolVersion {
		return fmt.Errorf("unsupported envelope version %d (implementation speaks %d)", e.Version, ProtocolVersion)
	}
	if err := ValidateTaskID(e.Task); err != nil {
		return err
	}
	if e.Attempt < 1 || e.Attempt > MaxAttempt {
		return fmt.Errorf("attempt %d out of range [1,%d]", e.Attempt, MaxAttempt)
	}
	if err := ValidateOID(e.Base); err != nil {
		return fmt.Errorf("base: %w", err)
	}
	if e.Depth < 0 || e.Depth > MaxHookDepth {
		return fmt.Errorf("depth %d out of range [0,%d]", e.Depth, MaxHookDepth)
	}
	if e.Agent != "" {
		if err := ValidateTaskID(e.Agent); err != nil {
			return fmt.Errorf("agent: %w", err)
		}
	}
	switch e.Relay {
	case "", RelaySync, RelayAsync:
	default:
		return fmt.Errorf("relay %q must be %q or %q", e.Relay, RelaySync, RelayAsync)
	}
	return nil
}

// Encode renders the envelope as push options: the version tag followed by
// key=value pairs. It validates first so a malformed envelope never leaves
// the process.
func (e Envelope) Encode() ([]string, error) {
	if e.Version == 0 {
		e.Version = ProtocolVersion
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	opts := []string{
		EnvelopeVersionTag,
		"task=" + e.Task,
		"attempt=" + strconv.Itoa(e.Attempt),
		"base=" + e.Base,
		"depth=" + strconv.Itoa(e.Depth),
	}
	if e.Agent != "" {
		opts = append(opts, "agent="+e.Agent)
	}
	if e.Relay != "" {
		opts = append(opts, "relay="+string(e.Relay))
	}
	return opts, nil
}

// DecodeEnvelope parses push options into an Envelope. Absent envelope, an
// unknown version tag, and unknown keys are all errors: a receiver rejects
// what it does not understand (R4.1).
func DecodeEnvelope(opts []string) (Envelope, error) {
	if len(opts) == 0 {
		return Envelope{}, fmt.Errorf("push carries no envelope (no push options)")
	}
	if len(opts) > maxPushOptions {
		return Envelope{}, fmt.Errorf("push carries %d options, more than the %d the protocol allows", len(opts), maxPushOptions)
	}
	if opts[0] != EnvelopeVersionTag {
		return Envelope{}, fmt.Errorf("push option 0 is %q, not the version tag %q", opts[0], EnvelopeVersionTag)
	}
	e := Envelope{Version: ProtocolVersion}
	seen := map[string]bool{}
	for _, opt := range opts[1:] {
		key, value, found := strings.Cut(opt, "=")
		if !found || value == "" {
			return Envelope{}, fmt.Errorf("push option %q is not key=value", opt)
		}
		if seen[key] {
			return Envelope{}, fmt.Errorf("push option key %q repeated", key)
		}
		seen[key] = true
		if err := e.setField(key, value); err != nil {
			return Envelope{}, err
		}
	}
	for _, required := range []string{"task", "attempt", "base", "depth"} {
		if !seen[required] {
			return Envelope{}, fmt.Errorf("envelope is missing required key %q", required)
		}
	}
	if err := e.Validate(); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

func (e *Envelope) setField(key, value string) error {
	switch key {
	case "task":
		e.Task = value
	case "attempt":
		n, err := ParseAttempt(value)
		if err != nil {
			return err
		}
		e.Attempt = n
	case "base":
		e.Base = value
	case "depth":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("depth %q is not an integer", value)
		}
		e.Depth = n
	case "agent":
		e.Agent = value
	case "relay":
		e.Relay = RelayMode(value)
	default:
		return fmt.Errorf("unknown envelope key %q", key)
	}
	return nil
}

// EnvelopeFromEnv decodes the envelope receive-pack exposes to hooks via
// GIT_PUSH_OPTION_COUNT / GIT_PUSH_OPTION_<n> (§1.2). getenv is injectable
// for tests; pass os.Getenv in hooks.
func EnvelopeFromEnv(getenv func(string) string) (Envelope, error) {
	countStr := getenv("GIT_PUSH_OPTION_COUNT")
	if countStr == "" {
		return Envelope{}, fmt.Errorf("push carries no envelope (GIT_PUSH_OPTION_COUNT unset; is receive.advertisePushOptions on?)")
	}
	count, err := strconv.Atoi(countStr)
	if err != nil || count < 0 || count > maxPushOptions {
		return Envelope{}, fmt.Errorf("GIT_PUSH_OPTION_COUNT %q out of range [0,%d]", countStr, maxPushOptions)
	}
	opts := make([]string, 0, count)
	for i := 0; i < count; i++ {
		opts = append(opts, getenv("GIT_PUSH_OPTION_"+strconv.Itoa(i)))
	}
	return DecodeEnvelope(opts)
}

// MatchesRef enforces envelope↔ref agreement (R4.1): a receiver rejects a
// push whose task or attempt disagree with the ref being written.
func (e Envelope) MatchesRef(info RefInfo) error {
	if e.Task != info.Task {
		return fmt.Errorf("envelope task %q disagrees with ref task %q", e.Task, info.Task)
	}
	if e.Attempt != info.Attempt {
		return fmt.Errorf("envelope attempt %d disagrees with ref attempt %d", e.Attempt, info.Attempt)
	}
	return nil
}

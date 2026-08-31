package api

// ObservationSchemaV1 is the machine contract emitted by captain prompt observe.
// Captain reports execution evidence; consumers such as Gavel own conformance
// policy and pass/fail decisions.
const ObservationSchemaV1 = "captain.observation/v1"

type ObservationFactState string

const (
	ObservationFactKnown       ObservationFactState = "known"
	ObservationFactUnset       ObservationFactState = "unset"
	ObservationFactUnknown     ObservationFactState = "unknown"
	ObservationFactUnsupported ObservationFactState = "unsupported"
)

type ObservationCaptureStatus string

const (
	ObservationCaptureComplete     ObservationCaptureStatus = "complete"
	ObservationCapturePartial      ObservationCaptureStatus = "partial"
	ObservationCaptureNotRequested ObservationCaptureStatus = "not_requested"
	ObservationCaptureUnavailable  ObservationCaptureStatus = "unavailable"
	ObservationCaptureUnsupported  ObservationCaptureStatus = "unsupported"
)

// RuntimeObservation is the versioned, machine-readable evidence produced by a
// single captain prompt observe execution. It reports facts for consumers to
// evaluate without assigning a conformance pass or fail result.
type RuntimeObservation struct {
	// SchemaVersion identifies the observation contract used to encode this document.
	SchemaVersion string `json:"schemaVersion"`
	// ObservationID uniquely identifies this document and its execution.
	ObservationID string `json:"observationId"`
	// Runtime records the selector requested by the caller and the runtime Captain resolved.
	Runtime ObservationRuntime `json:"runtime"`
	// Availability reports whether the resolved runtime is currently usable, or
	// why it is not, such as missing credentials, authentication, or an executable.
	// It is separate from Execution: an available runtime can still fail a
	// particular invocation because of a timeout, rate limit, or provider error.
	Availability Availability `json:"availability"`
	// Execution reports the outcome of this invocation: not_started, completed,
	// or failed, together with its duration and any classified error. It remains
	// not_started when availability or an unsupported control prevents dispatch.
	Execution ObservationExecution `json:"execution"`
	// Controls reports configuration knobs sent to the model; v1 contains only
	// reasoning effort. For each knob it records the caller's requested value,
	// Captain's resolved value, and the value found in the native provider request.
	// For example, requested=high and resolved=high but observed=unset means the
	// provider request omitted the requested effort.
	Controls ObservationControls `json:"controls"`
	// Capture is a bounded audit trail of activity around model execution. It
	// records provider dispatches, permission decisions, tool calls, MCP requests,
	// and Kubernetes requests as normalized events without prompt or tool bodies.
	// Each channel's status qualifies its events: complete with no events proves no
	// activity crossed that observed boundary, while unavailable or unsupported
	// means an empty list is inconclusive.
	Capture ObservationCapture `json:"capture"`
	// Metrics contains normalized duration, cost, and disjoint token-usage facts when available.
	Metrics ObservationMetrics `json:"metrics"`
}

type ObservationRuntime struct {
	Requested ObservationRuntimeRequested `json:"requested"`
	Resolved  ObservationRuntimeResolved  `json:"resolved"`
}

type ObservationRuntimeRequested struct {
	Selector string `json:"selector"`
}

type ObservationRuntimeResolved struct {
	Provider string      `json:"provider"`
	Backend  Backend     `json:"backend"`
	Mode     RuntimeMode `json:"mode"`
	Model    string      `json:"model"`
}

type ObservationExecution struct {
	State      string            `json:"state"`
	DurationMS *int64            `json:"durationMs,omitempty"`
	Error      *ObservationError `json:"error"`
}

type ObservationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ObservationControls struct {
	ReasoningEffort ObservationControl `json:"reasoningEffort"`
}

type ObservationControl struct {
	Requested ObservationStringFact `json:"requested"`
	Resolved  ObservationStringFact `json:"resolved"`
	Observed  ObservationStringFact `json:"observed"`
}

type ObservationStringFact struct {
	State        ObservationFactState `json:"state"`
	Value        *string              `json:"value,omitempty"`
	ReasonCode   string               `json:"reasonCode,omitempty"`
	EvidenceRefs []string             `json:"evidenceRefs,omitempty"`
}

type ObservationCapture struct {
	Dispatch    ObservationDispatchCapture   `json:"dispatch"`
	Permissions ObservationPermissionCapture `json:"permissions"`
	Tools       ObservationToolCapture       `json:"tools"`
	MCP         ObservationExternalCapture   `json:"mcp"`
	Kubernetes  ObservationExternalCapture   `json:"kubernetes"`
}

type ObservationDispatchCapture struct {
	Status ObservationCaptureStatus   `json:"status"`
	Events []ObservationDispatchEvent `json:"events"`
}

type ObservationDispatchEvent struct {
	ID       string `json:"id"`
	Attempt  int    `json:"attempt"`
	Boundary string `json:"boundary"`
}

type ObservationPermissionCapture struct {
	Status ObservationCaptureStatus     `json:"status"`
	Events []ObservationPermissionEvent `json:"events"`
}

type ObservationPermissionEvent struct {
	ID         string `json:"id"`
	ToolCallID string `json:"toolCallId,omitempty"`
	Tool       string `json:"tool"`
	Decision   string `json:"decision"`
	DecidedBy  string `json:"decidedBy"`
}

type ObservationToolCapture struct {
	Status ObservationCaptureStatus `json:"status"`
	Events []ObservationToolEvent   `json:"events"`
}

type ObservationToolEvent struct {
	ID         string                   `json:"id"`
	ToolCallID string                   `json:"toolCallId,omitempty"`
	Name       string                   `json:"name"`
	Execution  ObservationToolExecution `json:"execution"`
}

type ObservationToolExecution struct {
	State string `json:"state"`
}

// ObservationExternalCapture reports only traffic routed through a
// Captain-owned MCP or Kubernetes interception point.
type ObservationExternalCapture struct {
	Status     ObservationCaptureStatus   `json:"status"`
	ReasonCode string                     `json:"reasonCode,omitempty"`
	Events     []ObservationExternalEvent `json:"events"`
}

type ObservationExternalEvent struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Target        string `json:"target,omitempty"`
	Method        string `json:"method,omitempty"`
	HTTPMethod    string `json:"httpMethod,omitempty"`
	Tool          string `json:"tool,omitempty"`
	Resource      string `json:"resource,omitempty"`
	Status        string `json:"status,omitempty"`
	DurationMS    *int64 `json:"durationMs,omitempty"`
	CorrelationID string `json:"correlationId,omitempty"`
	BodySHA256    string `json:"bodySha256,omitempty"`
}

type ObservationMetrics struct {
	DurationMS ObservationNumberFact `json:"durationMs"`
	CostUSD    ObservationCostFact   `json:"costUSD"`
	Usage      ObservationUsageFact  `json:"usage"`
}

type ObservationNumberFact struct {
	State ObservationFactState `json:"state"`
	Value *int64               `json:"value,omitempty"`
	Unit  string               `json:"unit"`
}

type ObservationCostFact struct {
	State  ObservationFactState `json:"state"`
	Value  *float64             `json:"value,omitempty"`
	Unit   string               `json:"unit"`
	Source string               `json:"source,omitempty"`
}

type ObservationUsageFact struct {
	State     ObservationFactState     `json:"state"`
	Semantics string                   `json:"semantics"`
	Buckets   *ObservationUsageBuckets `json:"buckets,omitempty"`
}

type ObservationUsageBuckets struct {
	InputTokens      int `json:"inputTokens"`
	OutputTokens     int `json:"outputTokens"`
	ReasoningTokens  int `json:"reasoningTokens"`
	CacheReadTokens  int `json:"cacheReadTokens"`
	CacheWriteTokens int `json:"cacheWriteTokens"`
}

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

type RuntimeObservation struct {
	SchemaVersion string                `json:"schemaVersion"`
	ObservationID string                `json:"observationId"`
	Runtime       ObservationRuntime    `json:"runtime"`
	Availability  Availability          `json:"availability"`
	Execution     ObservationExecution  `json:"execution"`
	Controls      ObservationControls   `json:"controls"`
	Capture       ObservationCapture    `json:"capture"`
	Metrics       ObservationMetrics    `json:"metrics"`
	Artifacts     []ObservationArtifact `json:"artifacts"`
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

type ObservationArtifact struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Path      string `json:"path,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
}

package workspace

import "time"

const MaxExecutionTraceBytes = 8 << 20
const MaxExecutionTraceFrames = 32
const MaxExecutionTraceTargets = 128
const MaxExecutionTraceRedactions = 16

type ExecutionTracePolicy struct {
	Mode          string   `json:"mode"`
	CaptureValues bool     `json:"capture_values"`
	MaxEvents     int      `json:"max_events"`
	RedactNames   []string `json:"redact_names,omitempty"`
}
type ExecutionTraceFrame struct {
	FunctionHash string `json:"function_hash,omitempty"`
	TokenOffset  int    `json:"token_offset,omitempty"`
	Path         string `json:"path,omitempty"`
	Line         int    `json:"line"`
	Column       int    `json:"column,omitempty"`
	Name         string `json:"name,omitempty"`
	SourceHash   string `json:"source_hash,omitempty"`
	SourceHandle string `json:"source_handle,omitempty"`
	Mapping      string `json:"mapping"`
}
type ExecutionTraceValue struct {
	Name      string `json:"name"`
	Type      string `json:"type,omitempty"`
	Value     string `json:"value"`
	Redacted  bool   `json:"redacted"`
	Truncated bool   `json:"truncated"`
}
type ExecutionTraceEvent struct {
	Mutations []ExecutionMutation   `json:"mutations,omitempty"`
	Sequence  int                   `json:"sequence"`
	At        time.Time             `json:"at"`
	Kind      string                `json:"kind"`
	Thread    int                   `json:"thread,omitempty"`
	Reason    string                `json:"reason,omitempty"`
	Frames    []ExecutionTraceFrame `json:"frames"`
	Values    []ExecutionTraceValue `json:"values,omitempty"`
}
type ExecutionTraceTarget struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Verified bool   `json:"verified"`
}
type ExecutionTrace struct {
	ID               string                 `json:"trace_id"`
	Digest           string                 `json:"digest,omitempty"`
	WorkspaceID      ID                     `json:"workspace_id"`
	Epoch            uint64                 `json:"epoch"`
	Revision         string                 `json:"revision"`
	Policy           ExecutionTracePolicy   `json:"policy"`
	Started          time.Time              `json:"started"`
	Finished         *time.Time             `json:"finished,omitempty"`
	Completion       string                 `json:"completion,omitempty"`
	Launch           map[string]string      `json:"launch"`
	SourceHashes     map[string]string      `json:"source_hashes"`
	Targets          []ExecutionTraceTarget `json:"targets"`
	Events           []ExecutionTraceEvent  `json:"events"`
	LastStopSequence int                    `json:"last_stop_sequence"`
	Dropped          int                    `json:"dropped_events"`
	DroppedValues    int                    `json:"dropped_values"`
	Coverage         ExecutionCoverage      `json:"coverage"`
	Warnings         []string               `json:"warnings"`
}

func validExecutionTraceMetadata(trace ExecutionTrace) bool {
	if len(trace.Launch) > 16 || len(trace.SourceHashes) > MaxExecutionNodes || len(trace.Targets) > MaxExecutionTraceTargets || len(trace.Revision) > 256 {
		return false
	}
	for key, value := range trace.Launch {
		if len(key) > 128 || len(value) > 1024 {
			return false
		}
	}
	for path, hash := range trace.SourceHashes {
		if len(path) > 1024 || len(hash) > 64 {
			return false
		}
	}
	for _, target := range trace.Targets {
		if len(target.Path) > 1024 || target.Line < 1 {
			return false
		}
	}
	return true
}

func NormalizeExecutionTracePolicy(policy ExecutionTracePolicy) (ExecutionTracePolicy, error) {
	if policy.Mode == "" {
		policy.Mode = "stops"
	}
	switch policy.Mode {
	case "stops", "path", "conditions":
	case "mutations":
		return policy, Codedf("trace_capability_unavailable", "start a value-enabled conditions trace at a local scope, inspect mutation_capabilities, then use debug_breakpoints action=watch; automatic launch-time watchpoint resolution is unavailable")
	default:
		return policy, Codedf("trace_policy_invalid", "unknown trace policy")
	}
	if policy.MaxEvents == 0 {
		policy.MaxEvents = MaxExecutionTraceEvents
	}
	if policy.MaxEvents < 1 || policy.MaxEvents > MaxExecutionTraceEvents || len(policy.RedactNames) > MaxExecutionTraceRedactions {
		return policy, Codedf("trace_policy_invalid", "trace policy exceeds event or redaction bounds")
	}
	for _, name := range policy.RedactNames {
		if len(name) > 128 {
			return policy, Codedf("trace_policy_invalid", "redaction names must be at most 128 bytes")
		}
	}
	return policy, nil
}

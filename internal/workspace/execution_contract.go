package workspace

// Execution answers keep possibilities separate from observations. In
// particular, a missing event says something about one recording, while a
// missing static path says nothing unless every required contributor finished.
type ExecutionStatus string

const (
	ExecutionStaticPossible        ExecutionStatus = "static_possible"
	ExecutionObserved              ExecutionStatus = "observed"
	ExecutionNotObserved           ExecutionStatus = "not_observed"
	ExecutionStaticallyUnreachable ExecutionStatus = "statically_unreachable"
	ExecutionUnknown               ExecutionStatus = "unknown"
)

// These are ceilings, not promises of completeness. A request may spend less;
// reaching any ceiling must remain visible in the result's coverage.
const (
	MaxExecutionPaths              = 16
	MaxExecutionDepth              = 64
	MaxExecutionNodes              = 4000
	MaxExecutionEdges              = 20000
	MaxExecutionBytes              = 8 << 20
	MaxExecutionFunctionNodes      = 256
	MaxExecutionAnalysisMillis     = 20000
	MaxExecutionTraceEvents        = 10000
	MaxExecutionValues             = 128
	MaxExecutionValueBytes         = 1024
	MaxExecutionRetainedValueBytes = 64 << 10
	MaxExecutionRecursion          = 2
	MaxExecutionCycleVisits        = 1
	MaxExecutionTraces             = 16
)

// GraphBudget participates in snapshot identity. A smaller search and a
// larger one must never share a cached completeness claim.
type GraphBudget struct {
	Paths              int `json:"paths"`
	Depth              int `json:"depth"`
	Nodes              int `json:"nodes"`
	Edges              int `json:"edges"`
	Bytes              int `json:"bytes"`
	FunctionNodes      int `json:"function_nodes"`
	AnalysisMillis     int `json:"analysis_millis"`
	TraceEvents        int `json:"trace_events"`
	Values             int `json:"values"`
	ValueBytes         int `json:"value_bytes"`
	RetainedValueBytes int `json:"retained_value_bytes"`
	Recursion          int `json:"recursion"`
	CycleVisits        int `json:"cycle_visits"`
}

// DefaultGraphBudget returns a value rather than shared mutable configuration.
func DefaultGraphBudget() GraphBudget {
	return GraphBudget{
		Paths: MaxExecutionPaths, Depth: MaxExecutionDepth,
		Nodes: MaxExecutionNodes, Edges: MaxExecutionEdges, Bytes: MaxExecutionBytes,
		FunctionNodes: MaxExecutionFunctionNodes, AnalysisMillis: MaxExecutionAnalysisMillis,
		TraceEvents: MaxExecutionTraceEvents, Values: MaxExecutionValues,
		ValueBytes: MaxExecutionValueBytes, RetainedValueBytes: MaxExecutionRetainedValueBytes,
		Recursion: MaxExecutionRecursion, CycleVisits: MaxExecutionCycleVisits,
	}
}

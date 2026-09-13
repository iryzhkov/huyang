package workspace

import "context"

// ExecutionGraph belongs to an AnalysisSnapshot. Builders own mutable facts;
// returned snapshots own copies and never share slices with their contributors.
type ExecutionGraph struct {
	ID       string            `json:"id"`
	Revision string            `json:"revision"`
	Budget   GraphBudget       `json:"budget"`
	Nodes    []ExecutionNode   `json:"nodes"`
	Edges    []ExecutionEdge   `json:"edges"`
	Coverage ExecutionCoverage `json:"coverage"`
	Risks    []ExecutionRisk   `json:"risks"`
}

type ExecutionCoverage struct {
	Complete bool     `json:"complete"`
	Capped   bool     `json:"capped"`
	Limits   []string `json:"limits"`
	Gaps     []string `json:"gaps"`
}

type ExecutionRisk struct {
	Kind   string `json:"kind"`
	Node   string `json:"node,omitempty"`
	Detail string `json:"detail"`
}

// ExecutionEvidence is attached to nodes as well as edges. A declaration's
// location is evidence too, and cannot quietly move to a different revision.
type ExecutionEvidence struct {
	Revision       string            `json:"revision"`
	SourceHandles  []string          `json:"source_handles"`
	Producer       ProducerVersion   `json:"producer"`
	Confidence     string            `json:"confidence"`
	Classification string            `json:"classification"`
	EvidenceIDs    []string          `json:"evidence_ids"`
	Coverage       ExecutionCoverage `json:"coverage"`
}

type ExecutionNode struct {
	ID       string              `json:"id"`
	Kind     string              `json:"kind"`
	Path     string              `json:"path,omitempty"`
	Name     string              `json:"name,omitempty"`
	Line     int                 `json:"line,omitempty"`
	Evidence []ExecutionEvidence `json:"evidence"`
}

type ExecutionEdge struct {
	ID        string              `json:"id"`
	From      string              `json:"from"`
	To        string              `json:"to"`
	Kind      string              `json:"kind"`
	Condition string              `json:"condition,omitempty"`
	Evidence  []ExecutionEvidence `json:"evidence"`
}

type GraphStep struct {
	Node string `json:"node"`
	Edge string `json:"edge,omitempty"`
}

type GraphPath struct {
	ID    string      `json:"id"`
	Steps []GraphStep `json:"steps"`
}

type ExecutionPaths struct {
	Status   ExecutionStatus   `json:"status"`
	Paths    []GraphPath       `json:"paths"`
	Coverage ExecutionCoverage `json:"coverage"`
}

type ExecutionRequest struct {
	Key    AnalysisKey
	Root   string
	Budget GraphBudget
}

// ExecutionContributor keeps provider protocols outside the workspace core.
// Complete coverage must be explicitly declared by every contributor; returning
// no facts and no error does not mean the contributor proved an empty graph.
type ExecutionContributor interface {
	Name() string
	Version() string
	ContributeExecution(context.Context, ExecutionRequest, *ExecutionBuilder) error
}

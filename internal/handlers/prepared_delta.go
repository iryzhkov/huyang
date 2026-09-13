package handlers

// The delta a proposal makes, and who in it made it.
//
// A prepared diagnostic report on its own answers the wrong question: it says
// what is wrong with the staged code, not what this change made wrong. So the
// prepared findings are compared with the canonical ones for the same files,
// and each new finding is attributed to the operation whose bytes it landed
// in, the operation that targeted its file, or the operation the snapshot
// connects it to - or to the tool that wrote the file, or to nobody.

import (
	"context"
	"fmt"
	"strings"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// preparedDelta compares the prepared findings with the canonical ones and
// attributes what is new. A comparison it cannot make honestly is reported as
// such rather than guessed.
func (h *Handlers) preparedDelta(ctx context.Context, requestID string, workspace *workspacecore.Workspace, view providerpool.PreparedView, prepared []workspacecore.ComparableFinding, paths []string) *workspacecore.DiagnosticDelta {
	identity := workspace.Identity()
	base, complete := h.canonicalFindings(workspace, paths)
	plan, err := workspace.InspectPlan(view.PlanID, view.PlanRevision)
	if err != nil {
		return nil
	}
	input := workspacecore.AttributionInput{
		ToolPaths: toolPaths(plan),
		Targets:   operationTargets(workspace, plan),
	}
	if plan.Preview != nil {
		input.Hunks = plan.Preview.Hunks
	}
	// The impact path is the weakest evidence the ranking uses, so it is
	// built from the same snapshot everything else reads and skipped when
	// there is none.
	policy, _ := workspacecore.LoadPipelinePolicy(identity.Root, "")
	snapshot, snapshotErr := workspacecore.BuildAnalysisSnapshot(ctx, workspacecore.AnalysisRequest{
		Key: workspacecore.AnalysisKey{
			WorkspaceID: identity.ID, Epoch: identity.Epoch,
			Revision: fmt.Sprintf("wsrev_%d", identity.StateSeq), Profile: "attribution/v1",
			ConfigHash: workspacecore.PipelineFingerprint(policy),
		},
		Root: identity.Root, Changed: paths,
	}, workspacecore.NativeImportContributor{Policy: policy.Impact})
	if snapshotErr == nil {
		input.Reachable = workspacecore.ReachableFrom(snapshot, input.Targets)
	}
	delta := workspacecore.CompareDiagnostics(
		fmt.Sprintf("wsrev_%d", identity.StateSeq), view.PreparedRevision,
		base, prepared, complete, input)
	return &delta
}

// canonicalFindings are what the workspace already knew about these files.
//
// The second result says whether that baseline can be trusted, and the rule
// is stricter than it first looks: a file the ledger has never held any
// evidence about has no baseline at all. An empty report for such a file is
// silence, not a clean bill of health, and treating the two alike would
// present every pre-existing problem as something this change caused - which
// is the failure this whole comparison exists to prevent. A language server
// that has not finished indexing produces exactly that silence, which is how
// this was found.
func (h *Handlers) canonicalFindings(workspace *workspacecore.Workspace, paths []string) ([]workspacecore.ComparableFinding, bool) {
	report, err := workspace.Diagnostics("")
	if err != nil {
		return nil, false
	}
	wanted := map[string]bool{}
	for _, path := range paths {
		wanted[path] = true
	}
	var findings []workspacecore.ComparableFinding
	known := map[string]bool{}
	for _, group := range [][]workspacecore.DiagnosticItem{report.Current, report.Resolved} {
		for _, item := range group {
			// A document with a current or resolved finding has been looked
			// at against the bytes that are there now. Stale entries are
			// deliberately not counted: stale means the evidence describes
			// other bytes, which is the definition of not being a baseline.
			known[workspacePath(workspace, item.Document)] = true
		}
	}
	for _, item := range report.Current {
		path := workspacePath(workspace, item.Document)
		if !wanted[path] {
			continue
		}
		findings = append(findings, workspacecore.ComparableFinding{
			Path: path, Line: item.Finding.Range.StartLine + 1, Severity: item.Finding.Severity,
			Code: item.Finding.Code, Message: item.Finding.Message,
		})
	}
	if report.Confidence != workspacecore.ConfidenceAuthoritative {
		return findings, false
	}
	for _, path := range paths {
		if !known[path] {
			return findings, false
		}
	}
	return findings, true
}

// toolPaths are the files a tool wrote rather than an operation: the
// formatter, a code action, a generator. Their consequences belong to them.
func toolPaths(plan workspacecore.PlanRecord) map[string]string {
	paths := map[string]string{}
	if plan.Preparation == nil {
		return paths
	}
	for _, delta := range plan.Preparation.ToolDelta {
		classification := delta.Classification
		if classification == "" {
			classification = "tool"
		}
		paths[delta.Path] = classification
	}
	return paths
}

// operationTargets maps each file a plan operation names to the operations
// that name it.
func operationTargets(workspace *workspacecore.Workspace, plan workspacecore.PlanRecord) map[string][]string {
	targets := map[string][]string{}
	for _, operation := range plan.Operations {
		for _, path := range operationPaths(workspace, operation) {
			targets[path] = append(targets[path], operation.OpID)
		}
	}
	return targets
}

func operationPaths(workspace *workspacecore.Workspace, operation workspacecore.PlanOperation) []string {
	var paths []string
	for _, candidate := range []string{operation.Path, operation.From, operation.To} {
		if strings.TrimSpace(candidate) != "" {
			paths = append(paths, workspacePath(workspace, candidate))
		}
	}
	if operation.Target != nil {
		if operation.Target.SymbolLocator != nil {
			paths = append(paths, workspacePath(workspace, operation.Target.SymbolLocator.Path))
		}
		if operation.Target.FileRange != nil {
			paths = append(paths, operation.Target.FileRange.Path)
		}
	}
	return paths
}

// comparableFindings reads the kernel's per-file diagnostic reply into the
// shape the comparison takes.
func comparableFindings(path string, report any) []workspacecore.ComparableFinding {
	payload, _ := report.(map[string]any)
	var findings []workspacecore.ComparableFinding
	for _, raw := range mcpapi.AnySlice(payload["diagnostics"]) {
		entry, _ := raw.(map[string]any)
		if entry == nil || entry["message"] == nil {
			continue
		}
		findings = append(findings, workspacecore.ComparableFinding{
			Path: path, Line: argInt(entry, "line", 0),
			Severity: severityRank(fmt.Sprint(entry["severity"])),
			Code:     strings.TrimSpace(fmt.Sprint(entry["code"])),
			Message:  strings.Join(strings.Fields(fmt.Sprint(entry["message"])), " "),
		})
	}
	return findings
}

// severityRank maps the kernel's severity names onto the numbers the rest of
// the evidence model uses, where 1 is an error.
func severityRank(name string) int {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "ERROR":
		return 1
	case "WARN", "WARNING":
		return 2
	case "INFO", "INFORMATION":
		return 3
	case "HINT":
		return 4
	}
	return 0
}

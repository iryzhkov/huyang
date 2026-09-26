package handlers

// A prepared revision as a place to look.
//
// A plan that has been prepared holds exact bytes in a sandbox, and a language
// server has read them. Until now nothing could ask that server anything: an
// agent could see the diff and the verification verdict, but not read the
// staged file, not ask where a symbol went, not see the diagnostic in the
// place it appears. It had to apply the change to find out, which is the one
// thing a transaction exists to avoid.
//
// The rules this seam keeps:
//
//   - the sandbox path never leaves the service. A reply says the path the
//     agent knows, relative to the workspace, and the revision it read.
//   - a prepared handle names its prepared revision and is refused once that
//     revision is gone: edited, discarded, applied, or its provider restarted.
//     It never quietly relocates onto canonical source, because the bytes it
//     described do not exist there.
//   - listing is read-only. Nothing here stages, commits or mutates a prepared
//     revision that somebody has already reviewed.

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// preparedSelector is how a caller names the revision it wants to look at.
// An empty selector, "current", or the canonical revision itself all mean the
// workspace as it stands.
type preparedSelector struct {
	Revision     string
	PlanID       string
	PlanRevision uint64
}

func decodePreparedSelector(arguments map[string]any) preparedSelector {
	revision, _ := arguments["revision"].(string)
	planID, _ := arguments["plan_id"].(string)
	return preparedSelector{
		Revision: strings.TrimSpace(revision), PlanID: strings.TrimSpace(planID),
		PlanRevision: uintArgument(arguments["plan_revision"]),
	}
}

// wantsPrepared reports whether the selector names anything other than the
// canonical revision.
func (s preparedSelector) wantsPrepared(workspace *workspacecore.Workspace) bool {
	if s.PlanID != "" {
		return true
	}
	switch s.Revision {
	case "", "current", fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq):
		return false
	}
	return true
}

// resolvePrepared finds the prepared revision a selector names. The failure
// says which of the three ways to name one was used and why it did not
// resolve, because "not found" for a revision an agent prepared a moment ago
// is a question about the plan's state, not about the spelling.
func (h *Handlers) resolvePrepared(requestID string, workspace *workspacecore.Workspace, selector preparedSelector) (providerpool.PreparedView, map[string]any) {
	identity := workspace.Identity()
	if selector.PlanID != "" {
		stager := h.pool.Stager(identity.ID, selector.PlanID)
		if stager == nil {
			return providerpool.PreparedView{}, h.preparedGone(requestID, workspace, selector,
				fmt.Sprintf("plan %s has no prepared sandbox in this service", selector.PlanID))
		}
		view, ok := stager.Prepared()
		if !ok {
			return providerpool.PreparedView{}, h.preparedGone(requestID, workspace, selector,
				fmt.Sprintf("plan %s is not prepared; prepare it before reading its revision", selector.PlanID))
		}
		if selector.PlanRevision != 0 && selector.PlanRevision != view.PlanRevision {
			return providerpool.PreparedView{}, h.preparedGone(requestID, workspace, selector,
				fmt.Sprintf("plan %s is at revision %d, not %d; the preparation you read was replaced",
					selector.PlanID, view.PlanRevision, selector.PlanRevision))
		}
		return view, nil
	}
	for _, stager := range h.pool.StagersFor(identity.ID) {
		if view, ok := stager.Prepared(); ok && view.PreparedRevision == selector.Revision {
			stager.MarkUsed()
			return view, nil
		}
	}
	return providerpool.PreparedView{}, h.preparedGone(requestID, workspace, selector,
		fmt.Sprintf("no prepared revision %s is available; it was applied, discarded, re-prepared, or its provider restarted", selector.Revision))
}

// preparedGone is the one refusal this seam gives, and it never falls back to
// canonical: the bytes the caller asked about do not exist there.
func (h *Handlers) preparedGone(requestID string, workspace *workspacecore.Workspace, selector preparedSelector, detail string) map[string]any {
	result := mcpapi.Envelope(requestID, workspace, "conflict", "prepared_revision_unavailable", detail, map[string]any{
		"revision": selector.Revision, "plan_id": selector.PlanID,
	})
	result["next"] = []any{
		map[string]any{"tool": "change_plan", "action": "prepare", "plan_id": selector.PlanID, "use_new_idempotency_key": true},
		map[string]any{"tool": "read", "action": "read_canonical_instead"},
	}
	return result
}

// preparedPath is the absolute path of one workspace-relative path inside a
// prepared tree. It refuses anything that would leave the sandbox, so a
// crafted path cannot read the machine through a prepared revision.
func preparedPath(view providerpool.PreparedView, relative string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(relative))
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("path %q is outside the prepared revision", relative)
	}
	joined := filepath.Join(view.Tree, clean)
	if !strings.HasPrefix(joined, filepath.Clean(view.Tree)+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the prepared revision", relative)
	}
	return joined, nil
}

// canonicalFacing turns a path inside the prepared tree back into the path the
// agent knows. A location the service cannot place inside the tree is dropped
// rather than reported, because the alternative is handing back a sandbox
// path.
func canonicalFacing(view providerpool.PreparedView, path string) (string, bool) {
	if path == "" {
		return "", false
	}
	relative, err := filepath.Rel(view.Tree, path)
	if err != nil || strings.HasPrefix(relative, "..") {
		return "", false
	}
	return filepath.ToSlash(relative), true
}

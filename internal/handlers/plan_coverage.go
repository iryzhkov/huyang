package handlers

import (
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
	"strings"
)

// renameCoverageNote preserves the language server's evidence boundary.
func renameCoverageNote(plan workspacecore.PlanRecord) []string {
	for _, operation := range plan.Operations {
		if string(operation.Kind) == "rename_symbol" || strings.HasPrefix(operation.DerivedFrom, "rename_symbol ") {
			return []string{"Rename edits cover references returned by the language server, not every possible caller. Check remaining literal matches, including extensionless scripts, generated code and dynamic imports, before applying; review each match rather than replacing strings blindly."}
		}
	}
	return nil
}

package handlers

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// maxPathCandidates bounds the paths a read of a missing path suggests.
const maxPathCandidates = 5

// readFailure answers a read that failed. A read of a path the workspace does
// not hold is answered with the paths it does hold that the caller probably
// meant: a file an agent expected in one directory and found in another is
// the commonest read failure in the friction spool, and "no file at <path>"
// ends the line of enquiry where the path that does exist would continue it.
// Candidates are suggestions only; nothing is read in their place. symbol is
// the name path of a symbol read, so a suggestion names the same declaration
// in the candidate file. Every other failure is passed through as read_failed.
func readFailure(requestID string, workspace *workspacecore.Workspace, err error, symbol string) map[string]any {
	var missing *workspacecore.DocumentNotFoundError
	if workspacecore.ErrorCode(err) != workspacecore.CodeDocumentNotFound || !errors.As(err, &missing) {
		return mcpapi.Failure(requestID, workspace, "read_failed", err)
	}
	path := missing.Path
	data := map[string]any{"path": path}
	listed, coverage, listErr := workspace.Paths()
	if listErr != nil {
		return mcpapi.Envelope(requestID, workspace, "failed", workspacecore.CodeDocumentNotFound, fmt.Sprintf("No file at %s", path), data)
	}
	// The listing is what absence is claimed against, so it travels with
	// the claim: a capped listing cannot say a file exists nowhere.
	data["coverage"] = coverage
	named, near := pathCandidates(listed, path)
	candidates := append(append([]string(nil), named...), near...)
	if len(candidates) > maxPathCandidates {
		candidates = candidates[:maxPathCandidates]
	}
	var summary string
	switch {
	case len(named) > 0:
		summary = fmt.Sprintf("No file at %s; the workspace holds %s", path, strings.Join(candidates, ", "))
	case len(candidates) > 0:
		summary = fmt.Sprintf("No file at %s and no file of that name; similar paths: %s", path, strings.Join(candidates, ", "))
	case coverage.Complete:
		summary = fmt.Sprintf("No file at %s, and no file of that name anywhere in the workspace", path)
	case coverage.Capped:
		summary = fmt.Sprintf("No file at %s, and none of that name among the %d files listed; the listing stopped at its cap, so the workspace may still hold one", path, len(listed))
	default:
		summary = fmt.Sprintf("No file at %s, and none of that name among the %d files listed; the listing skipped some of the tree", path, len(listed))
	}
	result := mcpapi.Envelope(requestID, workspace, "failed", workspacecore.CodeDocumentNotFound, summary, data)
	if len(candidates) == 0 {
		result["next"] = []any{map[string]any{"tool": "search", "action": "locate_the_file_by_name", "query": pathBase(path)}}
		return result
	}
	data["candidates"] = candidates
	next := []any{}
	for _, candidate := range candidates[:min(2, len(candidates))] {
		step := map[string]any{"tool": "read", "action": "read_candidate", "path": candidate}
		if symbol != "" {
			step = map[string]any{"tool": "read", "action": "read_candidate", "target": map[string]any{
				"symbol_locator": map[string]any{"path": candidate, "name_path": symbol},
			}}
		}
		next = append(next, step)
	}
	result["next"] = next
	return result
}

// pathCandidates are the listed paths a caller who asked for path probably
// meant. named share its base name exactly. near are, in this order: files
// of the same stem in a directory of the same name as the requested one
// (read.go asked for, read_test.go or read.ts beside it), files of the same
// stem elsewhere, and
// base names within a small edit distance, case included. The stem ignores
// the extension and a _test, .test or .spec suffix, because the file an
// agent guesses and the one that exists are so often a source and its test
// or one language's spelling of another's. Within a rank the paths that
// share more of the requested path's directories, counted from the file up,
// come first, and otherwise listing order is kept.
func pathCandidates(listed []string, path string) (named, near []string) {
	base := pathBase(path)
	if base == "" {
		return nil, nil
	}
	type scored struct {
		path     string
		rank     int
		distance int
		shared   int
	}
	lowered, bound, stem := strings.ToLower(base), max(1, len(base)/5), fileStem(base)
	var exact, similar []scored
	for _, candidate := range listed {
		if candidate == path {
			continue
		}
		candidateBase := pathBase(candidate)
		shared := sharedDirectories(candidate, path)
		switch {
		case candidateBase == base:
			exact = append(exact, scored{path: candidate, shared: shared})
		case stem != "" && fileStem(candidateBase) == stem && shared > 0:
			similar = append(similar, scored{path: candidate, rank: 0, shared: shared})
		case stem != "" && fileStem(candidateBase) == stem:
			similar = append(similar, scored{path: candidate, rank: 1, shared: shared})
		default:
			if distance := editDistance(strings.ToLower(candidateBase), lowered, bound); distance <= bound {
				similar = append(similar, scored{path: candidate, rank: 2, distance: distance, shared: shared})
			}
		}
	}
	sort.SliceStable(exact, func(i, j int) bool { return exact[i].shared > exact[j].shared })
	sort.SliceStable(similar, func(i, j int) bool {
		if similar[i].rank != similar[j].rank {
			return similar[i].rank < similar[j].rank
		}
		if similar[i].distance != similar[j].distance {
			return similar[i].distance < similar[j].distance
		}
		return similar[i].shared > similar[j].shared
	})
	for _, candidate := range exact {
		named = append(named, candidate.path)
	}
	for _, candidate := range similar {
		near = append(near, candidate.path)
	}
	return named, near
}

// sharedDirectories counts the directories two paths have in common,
// matched from the file upwards: internal/handlers/read.go and
// handlers/read.go share one.
func sharedDirectories(left, right string) int {
	leftParts, rightParts := strings.Split(left, "/"), strings.Split(right, "/")
	shared := 0
	for l, r := len(leftParts)-2, len(rightParts)-2; l >= 0 && r >= 0 && leftParts[l] == rightParts[r]; l, r = l-1, r-1 {
		shared++
	}
	return shared
}

// editDistance is the Levenshtein distance between two strings, or bound+1
// as soon as it is certain to exceed bound.
func editDistance(left, right string, bound int) int {
	if diff := len(left) - len(right); diff > bound || -diff > bound {
		return bound + 1
	}
	previous := make([]int, len(right)+1)
	current := make([]int, len(right)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(left); i++ {
		current[0] = i
		smallest := current[0]
		for j := 1; j <= len(right); j++ {
			cost := 1
			if left[i-1] == right[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
			smallest = min(smallest, current[j])
		}
		if smallest > bound {
			return bound + 1
		}
		previous, current = current, previous
	}
	return previous[len(right)]
}

// fileStem is a base name without its extensions or a _test suffix, lower
// case: read.go, read_test.go, read.test.ts and Read.spec.ts share read.
func fileStem(base string) string {
	stem, _, _ := strings.Cut(base, ".")
	return strings.ToLower(strings.TrimSuffix(stem, "_test"))
}

func pathBase(path string) string {
	if index := strings.LastIndexByte(path, '/'); index >= 0 {
		return path[index+1:]
	}
	return path
}

package workspace

import (
	"fmt"
	"sort"
	"strings"
)

// LiteralChange is the result of replacing every located occurrence of a
// literal: the before and after image of each touched file, one patch per
// occurrence, and the document revisions the replacement produced.
type LiteralChange struct {
	Files        []PlanStageFile
	Patches      []string
	Replacements int
	Revisions    map[string]RevisionID
}

// ReplaceLiteral replaces the given occurrences with replacement, later
// occurrences first so earlier byte offsets stay valid. With preview the
// canonical bytes are left alone and the after images are simulated.
func (w *Workspace) ReplaceLiteral(workspaceID ID, hits []LiteralHit, replacement []byte, preview bool) (LiteralChange, error) {
	change := LiteralChange{Revisions: map[string]RevisionID{}}
	byPath := map[string][]LiteralHit{}
	var order []string
	for _, hit := range hits {
		if _, seen := byPath[hit.Path]; !seen {
			order = append(order, hit.Path)
		}
		byPath[hit.Path] = append(byPath[hit.Path], hit)
	}
	for _, path := range order {
		occurrences := byPath[path]
		sort.Slice(occurrences, func(i, j int) bool { return occurrences[i].ByteStart > occurrences[j].ByteStart })
		file, patches, err := w.replaceInFile(workspaceID, path, occurrences, replacement, preview)
		if err != nil {
			return change, err
		}
		change.Files = append(change.Files, file)
		change.Patches = append(change.Patches, patches...)
		change.Replacements += len(occurrences)
		if !preview {
			if snapshot, snapErr := w.Snapshot(path, ProviderLayer{}); snapErr == nil {
				change.Revisions[path] = snapshot.Revision
			}
		}
	}
	return change, nil
}

// replaceInFile applies the descending occurrences of one file.
func (w *Workspace) replaceInFile(workspaceID ID, path string, occurrences []LiteralHit, replacement []byte, preview bool) (PlanStageFile, []string, error) {
	read, err := w.Read(path)
	if err != nil {
		return PlanStageFile{}, nil, err
	}
	before := append([]byte(nil), read.Content...)
	after := append([]byte(nil), read.Content...)
	var patches []string
	for _, hit := range occurrences {
		if hit.ByteStart < 0 || hit.ByteEnd > len(after) || hit.ByteEnd < hit.ByteStart {
			return PlanStageFile{}, nil, fmt.Errorf("literal occurrence %s:%d is outside the document", path, hit.Line)
		}
		handle, rangeErr := w.NewRange(path, hit.ByteStart, hit.ByteEnd)
		if rangeErr != nil {
			return PlanStageFile{}, nil, rangeErr
		}
		if preview {
			patches = append(patches, exactDiff(read.Path, before, after, hit.ByteStart, hit.ByteEnd, replacement).Patch)
		} else {
			applied, _, applyErr := w.ApplyReplace(workspaceID, handle, replacement)
			if applyErr != nil {
				return PlanStageFile{}, nil, applyErr
			}
			patches = append(patches, applied.Diff.Patch)
		}
		after = append(append(append([]byte(nil), after[:hit.ByteStart]...), replacement...), after[hit.ByteEnd:]...)
	}
	return PlanStageFile{Path: read.Path, Before: before, After: after, BeforeExists: true, AfterExists: true, Patch: strings.Join(patches, "")}, patches, nil
}

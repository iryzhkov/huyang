package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// editDiffs is the per-file diff record of an edit response: the path and
// the content hashes before and after, which revision_diff and the replay
// receipts read back, plus the patch when asked for. The default response
// omits the patch: for a literal edit or a new file it would only echo what
// the agent just sent.
func editDiffs(applied appliedEdit, includePatch bool) []map[string]any {
	diffs := make([]map[string]any, 0, len(applied.files))
	for _, file := range applied.files {
		diff := map[string]any{
			"path": file.Path, "before_sha256": contentHash(file.Before), "after_sha256": contentHash(file.After),
		}
		if includePatch {
			diff["patch"] = boundedPatch(file.Patch)
		}
		diffs = append(diffs, diff)
	}
	return diffs
}

func contentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// maxPatchBytes bounds the patch text in an edit response. A whole-file
// rewrite would otherwise echo the old and new content back, which is the
// one thing the agent already has; the hashes stay exact and the full
// patch remains in the receipt for revision_diff.
const maxPatchBytes = 4096

func boundedPatch(patch string) string {
	if len(patch) <= maxPatchBytes {
		return patch
	}
	return patch[:maxPatchBytes] + fmt.Sprintf("\n... patch truncated, %d of %d bytes shown\n", maxPatchBytes, len(patch))
}

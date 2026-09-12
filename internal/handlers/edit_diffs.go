package handlers

import (
	"crypto/sha256"
	"encoding/hex"
)

// editDiffs is the per-file diff record of an edit response: the path, the
// content hashes before and after, and the patch. It is what revision_diff
// and the replay receipts read back, so it is present in every edit
// response regardless of verbosity.
func editDiffs(applied appliedEdit) []map[string]any {
	diffs := make([]map[string]any, 0, len(applied.files))
	for _, file := range applied.files {
		diffs = append(diffs, map[string]any{
			"path": file.Path, "before_sha256": contentHash(file.Before), "after_sha256": contentHash(file.After),
			"patch": file.Patch,
		})
	}
	return diffs
}

func contentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

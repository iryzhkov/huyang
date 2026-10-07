//go:build !unix

package runtimedir

import "os"

// ownerUID cannot read an owner outside Unix, so no directory is private
// there and every caller takes its temporary-directory fallback.
func ownerUID(os.FileInfo) (int, bool) { return 0, false }

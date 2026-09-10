//go:build !unix

package workspace

import "os"

func fileIdentity(os.FileInfo) (uint64, uint64) {
	return 0, 0
}

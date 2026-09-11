//go:build !unix

package workspace

import "os/exec"

func configureCommandCancellation(_ *exec.Cmd) {}

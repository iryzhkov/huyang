package workspace

import (
	"os/exec"

	"github.com/iryzhkov/huyang/internal/procguard"
)

// runCommandTree runs a verification command so that nothing it starts can
// outlive it: cancelling the context kills the command's whole tree, the
// descendants still running when the command exits are killed with it, and
// on Linux the tree also dies when this process dies, even by SIGKILL, when
// no context is ever cancelled. The command must come from
// exec.CommandContext.
func runCommandTree(command *exec.Cmd) error {
	tree, err := procguard.Wrap(command)
	if err != nil {
		return err
	}
	command.Cancel = func() error {
		tree.Stop()
		return nil
	}
	if err := tree.Start(); err != nil {
		return err
	}
	return tree.Wait()
}

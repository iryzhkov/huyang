package service

import (
	"errors"
	"flag"
	"fmt"
	"io"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// runTrust is the huyang trust subcommand: administration of the user
// policy that lets verify_run and prepared plans execute a repository's
// commands. It is a CLI and not a tool, because trust is configuration.
//
//	huyang trust <root>            add a root (stored absolute, symlink-resolved)
//	huyang trust --list            print the trusted roots
//	huyang trust --remove <root>   remove a root
//	huyang trust --config PATH ... use another policy file
func runTrust(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("huyang trust", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var list, remove bool
	var config string
	flags.BoolVar(&list, "list", false, "print the trusted roots")
	flags.BoolVar(&remove, "remove", false, "remove the named root")
	flags.StringVar(&config, "config", "", "policy file (default "+workspacecore.UserConfigPath()+")")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	usage := errors.New("usage: huyang trust [--config PATH] <root> | --list | --remove <root>")
	switch {
	case list:
		if flags.NArg() != 0 {
			return usage
		}
		roots, err := workspacecore.TrustedRoots(config)
		if err != nil {
			return err
		}
		for _, root := range roots {
			fmt.Fprintln(stdout, root)
		}
		return nil
	case remove:
		if flags.NArg() != 1 {
			return usage
		}
		path, err := workspacecore.UntrustRoot(config, flags.Arg(0))
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "removed %s from %s\n", flags.Arg(0), path)
		return nil
	default:
		if flags.NArg() != 1 {
			return usage
		}
		path, resolved, err := workspacecore.TrustRoot(config, flags.Arg(0))
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "trusted %s in %s; the running service picks it up on the next verify_run\n", resolved, path)
		return nil
	}
}

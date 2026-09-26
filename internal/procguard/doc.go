// Package procguard ties the whole process tree of a spawned command to the
// Huyang process that spawned it.
//
// Huyang starts two kinds of long or heavy children: the embedded Neovim
// provider, which in turn starts language servers, their helpers (cargo check,
// proc-macro servers, tsserver, Gradle) and debug adapters, and verification
// commands such as go test or cargo test, which start their own subprocesses.
// None of those descendants are direct children of Huyang, so neither a
// SIGKILL of the direct child nor a death signal on it reaches them, and when
// Huyang itself is killed outside a systemd unit they are orphaned.
//
// On Linux, Wrap rewrites a command so that it runs under a small guard: the
// same Huyang binary re-executed through /proc/self/exe. The guard leads a
// new process group, marks itself a child subreaper so that orphaned
// descendants are reparented to it rather than to init, and starts the real
// command with a death signal. It holds the read end of a pipe whose only
// write end stays in the owning Huyang process. When that pipe reports end of
// file (the owner closed it deliberately, exited, or was SIGKILLed) or the
// command exits, the guard kills every process in its group and every adopted
// descendant in its session, then exits the way the command did.
//
// Descendants that started a new session with setsid are deliberately spared:
// they asked to be detached, like the cargo target pruner's rm, which bounds
// its own work and is meant to finish after Neovim exits.
//
// On other platforms Wrap only places the command in its own process group
// (on Unix) so that Stop can kill the group, as verification commands did
// before.
package procguard

package handlers

import (
	"strings"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// describeCommands summarises the verification commands a workspace has,
// where they came from, whether change_plan prepare may execute them in its
// sandbox, and how to change that. It is part of the workspace_open response so an agent never
// has to discover the pipeline by trial.
func describeCommands(policy workspacecore.PipelinePolicy) map[string]any {
	source := "none"
	switch {
	case policy.ProjectConfig != "":
		source = ".huyang.toml"
	case len(policy.Detected) > 0:
		source = "detected"
	}
	commands := map[string]any{"source": source, "trusted": policy.Trusted}
	if len(policy.Detected) > 0 {
		commands["detected_from"] = policy.Detected
	}
	if len(policy.Format.Gate.Command) > 0 {
		commands["format_gate"] = strings.Join(policy.Format.Gate.Command, " ")
	}
	if len(policy.Check) > 0 {
		commands["check"] = commandLines(policy.Check)
	}
	if len(policy.Tests) > 0 {
		commands["tests"] = commandLines(policy.Tests)
	}
	state, reason := describePipelineState(policy)
	commands["state"] = state
	switch {
	case source == "none":
		commands["hint"] = reason
	case !policy.Trusted:
		commands["enable"] = "change_plan prepare runs these in its sandbox once this root is listed under [trust] roots in " + policy.UserConfig + "; run them in the shell meanwhile"
	default:
		commands["run"] = "change_plan prepare runs these against the staged sandbox; run them in the shell to check the canonical tree"
	}
	if source == "detected" {
		commands["override"] = "write .huyang.toml at the workspace root to declare your own commands"
	}
	return commands
}

func commandLines(commands []workspacecore.CommandPolicy) []string {
	lines := make([]string, 0, len(commands))
	for _, command := range commands {
		line := strings.Join(command.Command, " ")
		if command.Name != "" {
			line = command.Name + ": " + line
		}
		lines = append(lines, line)
	}
	return lines
}

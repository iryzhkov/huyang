package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// runHuyang replays every scenario for one language through Huyang, using
// the cheapest call sequence the current catalog allows.
func (s *session) runHuyang(spec languageSpec) {
	language := spec.Language
	root := s.fixtureDir(language)
	opened := s.call("W0", language, "workspace_open", map[string]any{"kind": "project", "root": root}, "open the fixture once per session")
	s.workspaces[language] = workspaceID(opened)
	// The commands block says whether verify_run may run them; the
	// capabilities block only lists what is not available.
	commands, _ := data(opened)["commands"].(map[string]any)
	s.pipelines[language] = commands["trusted"] == true && (commands["check"] != nil || commands["tests"] != nil)
	s.readScenarios(spec)
	s.editScenarios(spec)
}

func (s *session) readScenarios(spec languageSpec) {
	language, ws := spec.Language, s.workspaces[spec.Language]
	s.call("R1", language, "read", map[string]any{"workspace_id": ws, "target": map[string]any{"path": spec.CoreFile}}, "whole file by path")

	symbol := s.call("R2", language, "read", map[string]any{"workspace_id": ws, "target": map[string]any{"symbol_locator": map[string]any{"path": spec.ReportFile, "name_path": spec.Symbol}}}, "region by symbol name")
	if symbol["outcome"] != "ok" {
		found := s.call("R2", language, "search", map[string]any{"workspace_id": ws, "query": declarationQuery(spec), "mode": "literal"}, "fallback: locate the declaration")
		line := hitLine(found, 0)
		s.call("R2", language, "read", map[string]any{"workspace_id": ws, "target": map[string]any{"path": spec.ReportFile}, "start_line": line, "end_line": line + 22}, "fallback: line window")
	}

	s.call("R3", language, "search", map[string]any{"workspace_id": ws, "query": spec.CallQuery, "mode": "literal"}, "every call site")

	if s.features["read.targets"] {
		targets := make([]any, 0, len(spec.RelatedFile))
		for _, path := range spec.RelatedFile {
			targets = append(targets, map[string]any{"path": path})
		}
		s.call("R4", language, "read", map[string]any{"workspace_id": ws, "targets": targets}, "three files in one call")
	} else {
		for _, path := range spec.RelatedFile {
			s.call("R4", language, "read", map[string]any{"workspace_id": ws, "target": map[string]any{"path": path}}, "one file per call")
		}
	}
}

func declarationQuery(spec languageSpec) string {
	if spec.Language == "go" {
		return "func " + spec.Symbol + "("
	}
	return "def " + spec.Symbol + "("
}

func hitLine(envelope map[string]any, index int) int {
	hits := anySlice(data(envelope)["hits"])
	if index >= len(hits) {
		return 1
	}
	hit, _ := hits[index].(map[string]any)
	line, _ := hit["line"].(float64)
	return int(line)
}

func (s *session) editScenarios(spec languageSpec) {
	language := spec.Language
	s.literal("E1", spec, spec.E1, "one line in a known file")
	s.literal("E2", spec, spec.E2, "block located by content")
	s.renameLocal(spec)
	s.createFile(spec)
	for _, edit := range spec.E5 {
		s.literal("E5", spec, edit, "coordinated edit")
	}
	s.verify("E5", spec, "check+tests")
	s.literal("E6", spec, spec.E6Signature, "signature change; diagnostics arrive in the response")
	s.call("E6", language, "search", map[string]any{"workspace_id": s.workspaces[language], "query": spec.CallQuery, "mode": "literal"}, "find the callers")
	for _, edit := range spec.E6Callers {
		s.literal("E6", spec, edit, "fix one caller")
	}
	s.verify("E6", spec, "check")
	s.fileLifecycle(spec)
	s.verify("V1", spec, "tests")
}

// fileLifecycle is E7 (copy a file) and E8 (move a file and fix its
// importers): one call each when the catalog has the kinds, and otherwise
// the shell the agent would fall back to.
func (s *session) fileLifecycle(spec languageSpec) {
	language, ws := spec.Language, s.workspaces[spec.Language]
	if spec.CopyFrom != "" {
		if s.features["edit_apply.copy_file"] {
			s.call("E7", language, "edit_apply", map[string]any{"workspace_id": ws, "idempotency_key": "E7-" + language,
				"operation": map[string]any{"kind": "copy_file", "from": spec.CopyFrom, "to": spec.CopyTo}}, "copy in one call")
		} else {
			command := fmt.Sprintf("mkdir -p %s && cp %s %s", filepath.Dir(spec.CopyTo), spec.CopyFrom, spec.CopyTo)
			s.modelledFallback("E7", language, command, shell(s.fixtureDir(language), command))
		}
		s.verify("E7", spec, "check")
	}
	if spec.MoveFrom != "" {
		if s.features["edit_apply.move_file"] {
			s.call("E8", language, "edit_apply", map[string]any{"workspace_id": ws, "idempotency_key": "E8-" + language,
				"operation": map[string]any{"kind": "move_file", "from": spec.MoveFrom, "to": spec.MoveTo}}, "move in one call; the reply names the git add")
		} else {
			command := fmt.Sprintf("mv %s %s", spec.MoveFrom, spec.MoveTo)
			s.modelledFallback("E8", language, command, shell(s.fixtureDir(language), command))
		}
		for _, edit := range spec.E8Importers {
			s.literal("E8", spec, edit, "fix an importer")
		}
		s.verify("E8", spec, "tests")
	}
}

// literal applies one content-addressed edit, in one call when the catalog
// has replace_literal and otherwise through search plus edit_apply.
func (s *session) literal(scenario string, spec languageSpec, edit literalEdit, note string) {
	language, ws := spec.Language, s.workspaces[spec.Language]
	key := fmt.Sprintf("%s-%s-%d", scenario, language, len(s.calls))
	if s.features["edit_apply.replace_literal"] {
		operation := map[string]any{"kind": "replace_literal", "old": edit.Old, "new": edit.New}
		if edit.Path != "" {
			operation["path"] = edit.Path
		}
		if edit.Count > 0 {
			operation["expected_count"] = edit.Count
		}
		s.call(scenario, language, "edit_apply", map[string]any{"workspace_id": ws, "idempotency_key": key, "operation": operation}, note)
		return
	}
	found := s.call(scenario, language, "search", map[string]any{"workspace_id": ws, "query": edit.Old, "mode": "literal"}, "locate: "+note)
	hits := anySlice(data(found)["hits"])
	if edit.Path != "" && len(hits) > 1 {
		found = s.call(scenario, language, "search", map[string]any{"workspace_id": ws, "result_set_handle": resultSetHandle(found), "refine": map[string]any{"path": edit.Path}}, "narrow to the file")
		hits = anySlice(data(found)["hits"])
	}
	// Later handles first so earlier replacements do not shift them.
	for index := len(hits) - 1; index >= 0; index-- {
		hit, _ := hits[index].(map[string]any)
		s.call(scenario, language, "edit_apply", map[string]any{"workspace_id": ws, "idempotency_key": fmt.Sprintf("%s-%d", key, index),
			"operation": map[string]any{"kind": "replace_range", "target": map[string]any{"handle": hit["handle"]}, "content": edit.New}}, note)
	}
}

func resultSetHandle(envelope map[string]any) string {
	set, _ := data(envelope)["result_set"].(map[string]any)
	return fmt.Sprint(set["handle"])
}

// renameLocal is E3: five occurrences inside one function of one file.
func (s *session) renameLocal(spec languageSpec) {
	s.literal("E3", spec, spec.E3, "rename five occurrences")
}

// createFile is E4: a new ~40-line file.
func (s *session) createFile(spec languageSpec) {
	language, ws := spec.Language, s.workspaces[spec.Language]
	if s.features["edit_apply.create_file"] {
		s.call("E4", language, "edit_apply", map[string]any{"workspace_id": ws, "idempotency_key": "E4-" + language,
			"operation": map[string]any{"kind": "create_file", "path": spec.AuditFile, "content": spec.E4Content}}, "new file in one call")
		return
	}
	prepared := s.call("E4", language, "change_plan", map[string]any{"workspace_id": ws, "idempotency_key": "E4-prepare-" + language, "action": "prepare",
		"operations": []any{map[string]any{"op_id": "audit", "kind": "create_file", "path": spec.AuditFile, "content": spec.E4Content}}}, "prepare a one-operation plan")
	plan, _ := data(prepared)["plan"].(map[string]any)
	preparation, _ := plan["preparation"].(map[string]any)
	s.call("E4", language, "change_plan", map[string]any{"workspace_id": ws, "idempotency_key": "E4-apply-" + language, "action": "apply",
		"plan_id": plan["plan_id"], "plan_revision": plan["plan_revision"], "prepared_revision": preparation["prepared_revision"], "accept_provisional": true}, "apply the prepared plan")
}

// verify runs the named stages through verify_run when the workspace has
// executable commands, and otherwise falls back to the shell the agent
// would have to use.
func (s *session) verify(scenario string, spec languageSpec, stages string) {
	language, ws := spec.Language, s.workspaces[spec.Language]
	if s.pipelines[language] {
		list := []any{}
		for _, stage := range strings.Split(stages, "+") {
			list = append(list, stage)
		}
		s.call(scenario, language, "verify_run", map[string]any{"workspace_id": ws, "idempotency_key": fmt.Sprintf("%s-%s-verify-%d", scenario, language, len(s.calls)),
			"stages": list, "revision_or_transaction": "current", "test_scope": "full"}, "declared or detected commands")
		return
	}
	command := spec.Build
	if strings.Contains(stages, "tests") {
		command = spec.Test
	}
	output := shell(s.fixtureDir(language), command)
	s.modelledFallback(scenario, language, command, output)
}

// modelledFallback records the shell call an agent makes when verify_run is
// unavailable; it is attributed to the huyang family because that is what
// the agent using Huyang pays.
func (s *session) modelledFallback(scenario, language, command, output string) {
	request := fmt.Sprintf(`{"name":"bash","arguments":{"command":%q}}`, command)
	s.appendCall(Call{Scenario: scenario, Language: language, Family: "huyang", Tool: "bash (verify_run unavailable)",
		Note: "no .huyang.toml, no detected commands", RequestBytes: len(request), RequestTokens: s.tokens(request),
		ResponseBytes: len(output), ResponseTokens: s.tokens(output)})
	s.notes = append(s.notes, fmt.Sprintf("%s/%s: verify_run unavailable, fell back to `%s` in %s", scenario, language, command, filepath.Base(s.fixtureDir(language))))
}

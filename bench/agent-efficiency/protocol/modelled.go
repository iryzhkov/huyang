package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The modelled families work on the pristine copy under modelDir and apply
// the same edits there, so build and test outputs are real.

// runModelled replays every scenario for the built-in and Bash families.
func (s *session) runModelled(spec languageSpec) {
	for _, family := range []string{"builtin", "bash"} {
		s.modelFamily = family
		s.modelReads(spec, family)
		s.modelEdits(spec, family)
	}
}

func (s *session) modelFile(spec languageSpec, path string) string {
	content, _ := os.ReadFile(filepath.Join(s.modelDir(spec.Language), path))
	return string(content)
}

// numbered renders content the way the Read tool does: a right-aligned line
// number, a tab, the line.
func numbered(content string, first int) string {
	var b strings.Builder
	for index, line := range strings.Split(strings.TrimSuffix(content, "\n"), "\n") {
		fmt.Fprintf(&b, "%6d\t%s\n", first+index, line)
	}
	return b.String()
}

func request(tool string, arguments map[string]any) string {
	encoded, _ := json.Marshal(map[string]any{"name": tool, "arguments": arguments})
	return string(encoded)
}

// grepLines returns path:line:text for every line containing query.
func grepLines(root string, files []string, query string) string {
	var b strings.Builder
	for _, name := range files {
		content, _ := os.ReadFile(filepath.Join(root, name))
		for index, line := range strings.Split(string(content), "\n") {
			if strings.Contains(line, query) {
				fmt.Fprintf(&b, "%s:%d:%s\n", name, index+1, line)
			}
		}
	}
	return b.String()
}

func listFiles(root string) []string {
	var files []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		files = append(files, relative)
		return nil
	})
	return files
}

// lineWindow returns lines first..last of content, one-based inclusive.
func lineWindow(content string, first, last int) string {
	lines := strings.Split(content, "\n")
	if last > len(lines) {
		last = len(lines)
	}
	return strings.Join(lines[first-1:last], "\n") + "\n"
}

func (s *session) modelReads(spec languageSpec, family string) {
	language := spec.Language
	root := s.modelDir(language)
	core := s.modelFile(spec, spec.CoreFile)
	if family == "builtin" {
		s.modelled("R1", language, family, "Read", request("Read", map[string]any{"file_path": spec.CoreFile}), numbered(core, 1), "whole file")
	} else {
		s.modelled("R1", language, family, "bash", request("bash", map[string]any{"command": "cat " + spec.CoreFile}), core, "whole file")
	}

	report := s.modelFile(spec, spec.ReportFile)
	grep := grepLines(root, []string{spec.ReportFile}, declarationQuery(spec))
	line := 1
	fmt.Sscanf(strings.TrimPrefix(grep, spec.ReportFile+":"), "%d:", &line)
	if family == "builtin" {
		s.modelled("R2", language, family, "Grep", request("Grep", map[string]any{"pattern": declarationQuery(spec), "path": spec.ReportFile, "output_mode": "content", "-n": true}), grep, "locate the declaration")
		s.modelled("R2", language, family, "Read", request("Read", map[string]any{"file_path": spec.ReportFile, "offset": line, "limit": 22}), numbered(lineWindow(report, line, line+21), line), "line window")
	} else {
		s.modelled("R2", language, family, "bash", request("bash", map[string]any{"command": fmt.Sprintf("grep -n %q %s", declarationQuery(spec), spec.ReportFile)}), grep, "locate the declaration")
		s.modelled("R2", language, family, "bash", request("bash", map[string]any{"command": fmt.Sprintf("sed -n '%d,%dp' %s", line, line+21, spec.ReportFile)}), lineWindow(report, line, line+21), "line window")
	}

	calls := grepLines(root, listFiles(root), spec.CallQuery)
	if family == "builtin" {
		s.modelled("R3", language, family, "Grep", request("Grep", map[string]any{"pattern": spec.CallQuery, "output_mode": "content", "-n": true}), calls, "every call site")
	} else {
		s.modelled("R3", language, family, "bash", request("bash", map[string]any{"command": fmt.Sprintf("grep -rn %q .", spec.CallQuery)}), calls, "every call site")
	}

	if family == "builtin" {
		for _, path := range spec.RelatedFile {
			s.modelled("R4", language, family, "Read", request("Read", map[string]any{"file_path": path}), numbered(s.modelFile(spec, path), 1), "one file per call")
		}
	} else {
		var joined strings.Builder
		for _, path := range spec.RelatedFile {
			fmt.Fprintf(&joined, "==> %s <==\n%s\n", path, s.modelFile(spec, path))
		}
		s.modelled("R4", language, family, "bash", request("bash", map[string]any{"command": "head -n 2000 " + strings.Join(spec.RelatedFile, " ")}), joined.String(), "three files in one call")
	}
}

// editSnippet is what the Edit tool echoes back: the changed lines with four
// lines of context, numbered.
func editSnippet(content string, offset int, replacement string) string {
	before := strings.Count(content[:offset], "\n")
	first := before - 4
	if first < 1 {
		first = 1
	}
	last := before + strings.Count(replacement, "\n") + 5
	updated := content
	return numbered(lineWindow(updated, first, last), first)
}

// modelLiteral applies one edit to the model copy and records what the
// family pays for it.
func (s *session) modelLiteral(scenario string, spec languageSpec, family string, edit literalEdit, note string) {
	language := spec.Language
	root := s.modelDir(language)
	paths := []string{edit.Path}
	if edit.Path == "" {
		paths = listFiles(root)
	}
	for _, path := range paths {
		content := s.modelFile(spec, path)
		offset := strings.Index(content, edit.Old)
		if offset < 0 {
			continue
		}
		updated := strings.ReplaceAll(content, edit.Old, edit.New)
		_ = os.WriteFile(filepath.Join(root, path), []byte(updated), 0o644)
		if family == "builtin" {
			arguments := map[string]any{"file_path": path, "old_string": edit.Old, "new_string": edit.New}
			if edit.Count > 1 {
				arguments["replace_all"] = true
			}
			response := fmt.Sprintf("The file %s has been updated. Here's the result of running `cat -n` on a snippet of the edited file:\n%s", path, editSnippet(updated, offset, edit.New))
			s.modelled(scenario, language, family, "Edit", request("Edit", arguments), response, note)
		} else {
			script := fmt.Sprintf("python3 - <<'EOF'\nimport pathlib\np = pathlib.Path(%q)\ns = p.read_text()\nassert s.count(%q) == %d\np.write_text(s.replace(%q, %q))\nEOF", path, edit.Old, max(edit.Count, 1), edit.Old, edit.New)
			s.modelled(scenario, language, family, "bash", request("bash", map[string]any{"command": script}), "", note)
		}
	}
}

func (s *session) modelEdits(spec languageSpec, family string) {
	language := spec.Language
	root := s.modelDir(language)
	s.modelLiteral("E1", spec, family, spec.E1, "one line in a known file")
	s.modelLiteral("E2", spec, family, spec.E2, "block located by content")
	s.modelLiteral("E3", spec, family, spec.E3, "rename five occurrences")
	_ = os.WriteFile(filepath.Join(root, spec.AuditFile), []byte(spec.E4Content), 0o644)
	if family == "builtin" {
		s.modelled("E4", language, family, "Write", request("Write", map[string]any{"file_path": spec.AuditFile, "content": spec.E4Content}), "File created successfully at: "+spec.AuditFile, "new file")
	} else {
		s.modelled("E4", language, family, "bash", request("bash", map[string]any{"command": "cat > " + spec.AuditFile + " <<'EOF'\n" + spec.E4Content + "EOF"}), "", "new file")
	}
	for _, edit := range spec.E5 {
		s.modelLiteral("E5", spec, family, edit, "coordinated edit")
	}
	s.modelled("E5", language, family, "bash", request("bash", map[string]any{"command": spec.Build + " && " + spec.Test}), shell(root, spec.Build+" && "+spec.Test), "build and test")
	s.modelLiteral("E6", spec, family, spec.E6Signature, "signature change")
	s.modelled("E6", language, family, "bash", request("bash", map[string]any{"command": spec.Build}), shell(root, spec.Build), "see the compile errors")
	if family == "builtin" {
		s.modelled("E6", language, family, "Grep", request("Grep", map[string]any{"pattern": spec.CallQuery, "output_mode": "content", "-n": true}), grepLines(root, listFiles(root), spec.CallQuery), "find the callers")
	} else {
		s.modelled("E6", language, family, "bash", request("bash", map[string]any{"command": fmt.Sprintf("grep -rn %q .", spec.CallQuery)}), grepLines(root, listFiles(root), spec.CallQuery), "find the callers")
	}
	for _, edit := range spec.E6Callers {
		s.modelLiteral("E6", spec, family, edit, "fix one caller")
	}
	s.modelled("E6", language, family, "bash", request("bash", map[string]any{"command": spec.Build}), shell(root, spec.Build), "confirm the build")
	s.modelFileLifecycle(spec, family)
	s.modelled("V1", language, family, "bash", request("bash", map[string]any{"command": spec.Test}), shell(root, spec.Test), "run the tests")
}

// modelFileLifecycle is E7 and E8 for the modelled families. The built-in
// tools have no copy or move, so that family reads the file and writes it
// back (the content crosses the context twice) and deletes the source with
// bash; the bash family uses cp and git mv.
func (s *session) modelFileLifecycle(spec languageSpec, family string) {
	language := spec.Language
	root := s.modelDir(language)
	if spec.CopyFrom != "" {
		content := s.modelFile(spec, spec.CopyFrom)
		_ = os.MkdirAll(filepath.Join(root, filepath.Dir(spec.CopyTo)), 0o755)
		_ = os.WriteFile(filepath.Join(root, spec.CopyTo), []byte(content), 0o644)
		if family == "builtin" {
			s.modelled("E7", language, family, "Read", request("Read", map[string]any{"file_path": spec.CopyFrom}), numbered(content, 1), "read the source")
			s.modelled("E7", language, family, "Write", request("Write", map[string]any{"file_path": spec.CopyTo, "content": content}), "File created successfully at: "+spec.CopyTo, "write the copy")
		} else {
			command := fmt.Sprintf("mkdir -p %s && cp %s %s", filepath.Dir(spec.CopyTo), spec.CopyFrom, spec.CopyTo)
			s.modelled("E7", language, family, "bash", request("bash", map[string]any{"command": command}), "", "copy")
		}
		s.modelled("E7", language, family, "bash", request("bash", map[string]any{"command": spec.Build}), shell(root, spec.Build), "confirm the build")
	}
	if spec.MoveFrom != "" {
		content := s.modelFile(spec, spec.MoveFrom)
		_ = os.WriteFile(filepath.Join(root, spec.MoveTo), []byte(content), 0o644)
		_ = os.Remove(filepath.Join(root, spec.MoveFrom))
		if family == "builtin" {
			s.modelled("E8", language, family, "Read", request("Read", map[string]any{"file_path": spec.MoveFrom}), numbered(content, 1), "read the source")
			s.modelled("E8", language, family, "Write", request("Write", map[string]any{"file_path": spec.MoveTo, "content": content}), "File created successfully at: "+spec.MoveTo, "write the destination")
			s.modelled("E8", language, family, "bash", request("bash", map[string]any{"command": "rm " + spec.MoveFrom}), "", "remove the source")
		} else {
			s.modelled("E8", language, family, "bash", request("bash", map[string]any{"command": fmt.Sprintf("git mv %s %s", spec.MoveFrom, spec.MoveTo)}), "", "move")
		}
		for _, edit := range spec.E8Importers {
			s.modelLiteral("E8", spec, family, edit, "fix an importer")
		}
		s.modelled("E8", language, family, "bash", request("bash", map[string]any{"command": spec.Test}), shell(root, spec.Test), "run the tests")
	}
}

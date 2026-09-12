package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Trust policy administration for the huyang trust CLI. The user policy is
// the TOML file at UserConfigPath; TrustRoot and UntrustRoot edit its
// [trust].roots array textually, so the user's comments and layout
// survive, and validate the result by decoding it before writing.

// TrustedRoots returns the roots listed in the user policy, or none when
// the file does not exist.
func TrustedRoots(userConfig string) ([]string, error) {
	user, err := readUserPolicy(userConfig)
	if err != nil {
		return nil, err
	}
	return user.Trust.Roots, nil
}

// TrustRoot adds root to [trust].roots. The root must be an existing
// directory; it is stored absolute and symlink-resolved, which is how the
// policy matches it. A root already listed (directly or through a parent)
// is reported and not duplicated. The file is created with mode 0600 when
// absent. It returns the path written and the resolved root.
func TrustRoot(userConfig, root string) (string, string, error) {
	path := defaultUserConfigPath(userConfig)
	if path == "" {
		return "", "", errors.New("no user config path: set XDG_CONFIG_HOME or HOME")
	}
	resolved, err := resolveTrustRoot(root)
	if err != nil {
		return path, "", err
	}
	user, err := readUserPolicy(path)
	if err != nil {
		return path, resolved, err
	}
	for _, listed := range user.Trust.Roots {
		if existing, err := filepath.EvalSymlinks(listed); err == nil && (existing == resolved || insidePath(existing, resolved)) {
			return path, resolved, fmt.Errorf("%s is already trusted through %s", resolved, listed)
		}
	}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		content = nil
	} else if err != nil {
		return path, resolved, err
	}
	updated, err := insertTrustRoot(string(content), resolved)
	if err != nil {
		return path, resolved, err
	}
	return path, resolved, writeUserPolicy(path, updated, resolved, true)
}

// UntrustRoot removes the line that lists root (as given or resolved) from
// [trust].roots.
func UntrustRoot(userConfig, root string) (string, error) {
	path := defaultUserConfigPath(userConfig)
	content, err := os.ReadFile(path)
	if err != nil {
		return path, err
	}
	candidates := map[string]bool{root: true}
	if absolute, err := filepath.Abs(root); err == nil {
		candidates[absolute] = true
		if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
			candidates[resolved] = true
		}
	}
	lines := strings.Split(string(content), "\n")
	kept := make([]string, 0, len(lines))
	removed := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ","))
		if value, err := unquoteTOML(trimmed); err == nil && candidates[value] {
			removed = true
			continue
		}
		kept = append(kept, line)
	}
	if !removed {
		return path, fmt.Errorf("%s is not listed in %s", root, path)
	}
	return path, writeUserPolicy(path, strings.Join(kept, "\n"), "", false)
}

func resolveTrustRoot(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("%s: %w", root, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", resolved)
	}
	return resolved, nil
}

func readUserPolicy(userConfig string) (userPipelinePolicy, error) {
	var user userPipelinePolicy
	path := defaultUserConfigPath(userConfig)
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return user, nil
	}
	if err != nil {
		return user, err
	}
	if _, err := toml.Decode(string(content), &user); err != nil {
		return user, fmt.Errorf("invalid user policy %s: %w", path, err)
	}
	return user, nil
}

// insertTrustRoot adds one quoted root to the [trust].roots array of a
// TOML document, creating the table and array when they are absent and
// keeping every other line as it is. A single-line array is expanded to
// one element per line first.
func insertTrustRoot(content, root string) (string, error) {
	entry := "  " + quoteTOML(root) + ","
	lines := strings.Split(content, "\n")
	start := -1
	for index, line := range lines {
		if strings.TrimSpace(line) == "[trust]" {
			start = index
			break
		}
	}
	if start < 0 {
		body := strings.TrimRight(content, "\n")
		if body != "" {
			body += "\n\n"
		}
		return body + "[trust]\nroots = [\n" + entry + "\n]\n", nil
	}
	for index := start + 1; index < len(lines); index++ {
		trimmed := strings.TrimSpace(lines[index])
		if strings.HasPrefix(trimmed, "[") {
			break
		}
		if !strings.HasPrefix(trimmed, "roots") || !strings.Contains(trimmed, "=") {
			continue
		}
		if strings.HasSuffix(strings.TrimSpace(strings.SplitN(trimmed, "#", 2)[0]), "]") {
			return expandSingleLineRoots(lines, index, entry)
		}
		for end := index + 1; end < len(lines); end++ {
			if strings.TrimSpace(lines[end]) != "]" {
				continue
			}
			ensureTrailingComma(lines[index+1 : end])
			lines = append(lines[:end], append([]string{entry}, lines[end:]...)...)
			return strings.Join(lines, "\n"), nil
		}
		return "", errors.New("roots array has no closing bracket on its own line")
	}
	insert := []string{"roots = [", entry, "]"}
	lines = append(lines[:start+1], append(insert, lines[start+1:]...)...)
	return strings.Join(lines, "\n"), nil
}

// expandSingleLineRoots rewrites a `roots = [...]` line as one element per
// line and appends entry.
func expandSingleLineRoots(lines []string, index int, entry string) (string, error) {
	var single struct {
		Roots []string `toml:"roots"`
	}
	if _, err := toml.Decode(strings.TrimSpace(lines[index]), &single); err != nil {
		return "", fmt.Errorf("cannot parse %q: %w", lines[index], err)
	}
	expanded := []string{"roots = ["}
	for _, existing := range single.Roots {
		expanded = append(expanded, "  "+quoteTOML(existing)+",")
	}
	expanded = append(expanded, entry, "]")
	lines = append(lines[:index], append(expanded, lines[index+1:]...)...)
	return strings.Join(lines, "\n"), nil
}

// ensureTrailingComma makes the last element line of an array end with a
// comma so a new element can follow it; comment and blank lines are skipped.
func ensureTrailingComma(elements []string) {
	for index := len(elements) - 1; index >= 0; index-- {
		trimmed := strings.TrimSpace(elements[index])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		code := strings.TrimRight(strings.SplitN(trimmed, " #", 2)[0], " ")
		if !strings.HasSuffix(code, ",") {
			elements[index] = strings.Replace(elements[index], code, code+",", 1)
		}
		return
	}
}

// writeUserPolicy validates the edited document by decoding it (and, when
// adding, by checking the root is now listed), then writes it with mode
// 0600 through the package's atomic write.
func writeUserPolicy(path, content, mustList string, adding bool) error {
	var user userPipelinePolicy
	if _, err := toml.Decode(content, &user); err != nil {
		return fmt.Errorf("edited policy would be invalid, nothing written: %w", err)
	}
	if adding {
		listed := false
		for _, root := range user.Trust.Roots {
			listed = listed || root == mustList
		}
		if !listed {
			return errors.New("edited policy does not list the root, nothing written")
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicWriteFile(path, []byte(content), 0o600)
}

func quoteTOML(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(value) + `"`
}

func unquoteTOML(value string) (string, error) {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", errors.New("not a quoted string")
	}
	var decoded struct {
		Value string `toml:"value"`
	}
	if _, err := toml.Decode("value = "+value, &decoded); err != nil {
		return "", err
	}
	return decoded.Value, nil
}

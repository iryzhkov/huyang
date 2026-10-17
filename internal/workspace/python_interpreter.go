package workspace

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// ProjectPython is discovery metadata, not proof that dependencies are installed.
// Configured leaves interpreter selection to the project's Pyright configuration.
type ProjectPython struct {
	Interpreter string `json:"interpreter,omitempty"`
	Source      string `json:"source,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Configured  bool   `json:"configured,omitempty"`
}

const uvPythonQueryTimeout = 2 * time.Second

// Poetry's interpreter discovery can legitimately take more than two seconds.
// Keep its extra startup headroom bounded and subordinate to the caller context.
const poetryPythonQueryTimeout = 5 * time.Second

type pythonDiscovery struct {
	lookPath   func(string) (string, error)
	run        func(context.Context, string, string, ...string) (string, error)
	active     string
	userConfig string // empty selects the same user policy as the pipeline
}

// ResolveProjectPython reuses the pipeline's local-venv/lockfile selection.
// Local/configured selection reads files only. Manager queries require the
// canonical root to be trusted: uv/Poetry may execute candidate interpreters
// while locating an environment. Huyang never syncs or installs dependencies.
func ResolveProjectPython(ctx context.Context, root string) ProjectPython {
	return resolveProjectPython(ctx, root, pythonDiscovery{
		lookPath: exec.LookPath, run: queryPythonManager, active: os.Getenv("VIRTUAL_ENV"),
	})
}

func resolveProjectPython(ctx context.Context, root string, discovery pythonDiscovery) ProjectPython {
	root, err := filepath.Abs(root)
	if err != nil {
		return ProjectPython{Reason: "invalid_project_root"}
	}
	if projectConfiguresPython(root) {
		return ProjectPython{Source: "project_config", Configured: true}
	}
	environment := pythonEnvironment(root)
	if environment.venvBin != "" {
		python := filepath.Join(environment.venvBin, "python")
		if executablePython(python) {
			return ProjectPython{Interpreter: python, Source: environment.detected[0]}
		}
		// An invalid preferred venv is an environment defect; do not silently
		// substitute an unrelated interpreter and call its imports authoritative.
		return ProjectPython{Reason: "project_interpreter_not_executable"}
	}
	if discovery.active != "" {
		python := filepath.Join(discovery.active, "bin", "python")
		if filepath.IsAbs(python) && executablePython(python) {
			return ProjectPython{Interpreter: python, Source: "VIRTUAL_ENV"}
		}
	}
	if len(environment.runner) == 0 {
		return ProjectPython{Reason: "project_interpreter_not_found"}
	}
	manager := environment.runner[0]
	if !pythonManagerRootTrusted(root, discovery.userConfig) {
		return ProjectPython{Source: manager, Reason: "environment_manager_untrusted"}
	}
	command, err := discovery.lookPath(manager)
	if err != nil || !filepath.IsAbs(command) {
		return ProjectPython{Source: manager, Reason: "environment_manager_unavailable"}
	}
	// PATH entries pointing into the project must not turn discovery into
	// execution of a repository's uv/poetry script.
	realCommand, err := filepath.EvalSymlinks(command)
	if err != nil || pathWithin(root, realCommand) {
		return ProjectPython{Source: manager, Reason: "environment_manager_unavailable"}
	}
	args := []string{"--offline", "python", "find", "--no-python-downloads"}
	queryTimeout := uvPythonQueryTimeout
	if manager == "poetry" {
		args = []string{"--no-plugins", "env", "info", "--executable"}
		queryTimeout = poetryPythonQueryTimeout
	}
	queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	output, err := discovery.run(queryCtx, root, realCommand, args...)
	if err != nil {
		return ProjectPython{Source: manager, Reason: "environment_manager_query_failed"}
	}
	python := strings.TrimSpace(output)
	if strings.ContainsAny(python, "\r\n") || !filepath.IsAbs(python) || !executablePython(python) {
		return ProjectPython{Source: manager, Reason: "environment_manager_invalid_interpreter"}
	}
	if !pythonVirtualenv(python) {
		return ProjectPython{Source: manager, Reason: "environment_manager_not_project_environment"}
	}
	return ProjectPython{Interpreter: filepath.Clean(python), Source: manager}
}

// Match M5 pipeline trust: resolve both sides, allow an explicitly trusted
// root or its descendants, and fail closed on policy/root resolution errors.
// root is the canonical environment root, never the preparation sandbox.
func pythonManagerRootTrusted(root, userConfig string) bool {
	roots, err := TrustedRoots(userConfig)
	if err != nil {
		return false
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	for _, trusted := range roots {
		resolved, err := filepath.EvalSymlinks(trusted)
		if err == nil && (resolvedRoot == resolved || insidePath(resolved, resolvedRoot)) {
			return true
		}
	}
	return false
}

// Inspect the selected environment path, not the resolved Python binary:
// venv/bin/python commonly symlinks to a base interpreter outside that venv.
func pythonVirtualenv(python string) bool {
	bin := filepath.Dir(filepath.Clean(python))
	if filepath.Base(bin) != "bin" {
		return false
	}
	marker, err := os.Stat(filepath.Join(filepath.Dir(bin), "pyvenv.cfg"))
	return err == nil && marker.Mode().IsRegular()
}

func executablePython(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0
}

func pathWithin(root, path string) bool {
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// Treat an explicit environment setting conservatively even in JSONC. This
// only detects ownership; Pyright remains responsible for parsing its config.
var pythonJSONEnvironment = regexp.MustCompile(`"(venv|venvPath|pythonPath)"\s*:`)

func projectConfiguresPython(root string) bool {
	if content, err := os.ReadFile(filepath.Join(root, "pyrightconfig.json")); err == nil && pythonJSONEnvironment.Match(content) {
		return true
	}
	content, _ := os.ReadFile(filepath.Join(root, "pyproject.toml"))
	var config map[string]any
	metadata, err := toml.Decode(string(content), &config)
	if err != nil {
		return false
	}
	for _, server := range []string{"pyright", "basedpyright"} {
		for _, key := range []string{"venv", "venvPath", "pythonPath"} {
			if metadata.IsDefined("tool", server, key) {
				return true
			}
		}
	}
	return false
}

// Limit manager output and never surface its stderr (which may contain local
// configuration). The caller requires canonical-root trust before reaching
// this subprocess: a manager can inspect repository config and run interpreters.
func queryPythonManager(ctx context.Context, root, command string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = root
	cmd.WaitDelay = 100 * time.Millisecond
	var output strings.Builder
	cmd.Stdout = &limitedPythonOutput{writer: &output, remaining: 8192}
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return output.String(), nil
}

type limitedPythonOutput struct {
	writer    io.Writer
	remaining int
}

func (w *limitedPythonOutput) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, errors.New("environment manager output exceeded limit")
	}
	n, err := w.writer.Write(p)
	w.remaining -= n
	return n, err
}

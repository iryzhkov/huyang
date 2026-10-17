//go:build linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Opt-in only. This test never provisions tools, builds Huyang, contacts an
// installed service, or changes HOME. See tests/m9-language-probe/setup.sh.
// When enabled, every missing prerequisite is a failure, never a passing skip.
func TestPythonEnvironmentAcceptance(t *testing.T) {
	if os.Getenv("HUYANG_M9_PYTHON_PROBE") != "1" {
		t.Skip("set HUYANG_M9_PYTHON_PROBE=1 after job-local provisioning")
	}
	if os.Getenv("HUYANG_FRICTION") != "0" {
		t.Fatal("HUYANG_FRICTION=0 is required")
	}
	m9ScratchHome(t) // Before any writes or subprocesses, including preflight.
	tools := m9RequiredDir(t, "HUYANG_M9_TOOLS")
	out := m9RequiredDir(t, "HUYANG_M9_RUN")
	binary := os.Getenv("HUYANG_M9_BINARY")
	m9Executable(t, binary)
	realBinary, err := filepath.EvalSymlinks(binary)
	if err != nil || !strings.HasPrefix(realBinary, filepath.Dir(out)+string(filepath.Separator)) {
		t.Fatal("HUYANG_M9_BINARY must be a private build under the parent of HUYANG_M9_RUN")
	}
	if entries, err := os.ReadDir(out); err != nil || len(entries) != 0 {
		t.Fatal("HUYANG_M9_RUN must be an empty private directory")
	}
	tmp := os.Getenv("TMPDIR")
	tmpReal, tmpErr := filepath.EvalSymlinks(tmp)
	if !filepath.IsAbs(tmp) || tmpErr != nil || strings.HasPrefix(tmpReal+"/", "/tmp/") {
		t.Fatal("set an existing on-disk TMPDIR outside /tmp, including through symlinks")
	}
	scratch := filepath.Dir(out)
	if scratch == os.Getenv("HOME") || !m9Within(os.Getenv("HOME"), scratch) || !m9Within(scratch, tools) || !m9Within(scratch, tmpReal) {
		t.Fatal("tools and TMPDIR must be under the private run parent, distinct from HOME")
	}
	m9PrivateDir(t, scratch)
	m9PrivateDir(t, out)
	m9PrivateDir(t, tmpReal)
	beforeHome := m9HomeMetadata(t, scratch)
	beforeTools := m9ToolHashes(t, tools, realBinary)
	m9JSON(t, filepath.Join(out, "before-home-metadata.json"), beforeHome)
	m9JSON(t, filepath.Join(out, "before-tool-hashes.json"), beforeTools)
	t.Cleanup(func() {
		afterTools := m9ToolHashes(t, tools, realBinary)
		afterHome := m9HomeMetadata(t, scratch)
		m9JSON(t, filepath.Join(out, "after-tool-hashes.json"), afterTools)
		m9JSON(t, filepath.Join(out, "after-home-metadata.json"), afterHome)
		if !maps.Equal(beforeTools, afterTools) || !maps.Equal(beforeHome, afterHome) {
			t.Error("immutable tools or scratch HOME outside the allowed run parent changed")
		}
	})
	for _, name := range []string{"python", "python3", "uv", "poetry", "nvim", "node", "pyright-langserver", "gopls", "strace", "git", "sh", "bash", "readlink", "uname", "dirname", "realpath"} {
		m9Executable(t, filepath.Join(tools, "bin", name))
	}
	if _, err := os.Stat(filepath.Join(tools, "nvim-lspconfig", "lsp", "pyright.lua")); err != nil {
		t.Fatal(err)
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	// Only selected variables go to subprocesses: no credentials, user MCP
	// endpoints, Python paths, user package configs, or inherited venv.
	preflightTmp := filepath.Join(out, "preflight-tmp")
	if err := os.MkdirAll(preflightTmp, 0700); err != nil {
		t.Fatal(err)
	}
	baseEnv := []string{"HOME=" + os.Getenv("HOME"), "LANG=C.UTF-8", "HUYANG_FRICTION=0",
		"XDG_CONFIG_HOME=" + filepath.Join(out, "preflight-config"), "XDG_CACHE_HOME=" + filepath.Join(out, "preflight-cache"),
		"XDG_DATA_HOME=" + filepath.Join(out, "preflight-data"), "POETRY_CONFIG_DIR=" + filepath.Join(out, "preflight-poetry-config"),
		"POETRY_KEYRING_ENABLED=false", "NETRC=/dev/null", "PYTHONDONTWRITEBYTECODE=1",
		"TMPDIR=" + preflightTmp, "XDG_STATE_HOME=" + filepath.Join(out, "preflight-state"),
		"POETRY_CACHE_DIR=" + filepath.Join(out, "preflight-poetry-cache"), "M9_COMMAND_LOG=" + filepath.Join(out, "preflight-commands.log")}
	pins := map[string][]string{
		"python": {"--version"}, "uv": {"--version"}, "poetry": {"--version"},
		"nvim": {"--version"}, "node": {"--version"}, "gopls": {"version"},
	}
	versions := map[string]string{}
	for name, args := range pins {
		versions[name] = m9Exec(t, context.Background(), tools, append(baseEnv, "PATH="+filepath.Join(tools, "bin")), filepath.Join(tools, "bin", name), args...)
	}
	for name, want := range map[string]string{"python": "3.12.8", "uv": "0.9.5", "poetry": "2.2.1", "nvim": "0.12.5", "node": "22.14.0", "gopls": "0.23.0"} {
		if !strings.Contains(versions[name], want) {
			t.Fatalf("%s pin: want %s, got %q", name, want, versions[name])
		}
	}
	packageJSON, err := os.ReadFile(filepath.Join(tools, "npm", "node_modules", "pyright", "package.json"))
	if err != nil || !strings.Contains(string(packageJSON), `"version": "1.1.413"`) {
		t.Fatal("missing pinned pyright 1.1.413 package")
	}
	m9JSON(t, filepath.Join(out, "versions.json"), versions)
	built, err := os.ReadFile(realBinary)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(built)
	m9JSON(t, filepath.Join(out, "binary.json"), map[string]any{"path": realBinary, "sha256": hex.EncodeToString(digest[:])})
	for _, name := range []string{"manifest.txt", "download-sha256.txt", "npm/package-lock.json"} {
		body, err := os.ReadFile(filepath.Join(tools, name))
		if err != nil {
			t.Fatal(err)
		}
		m9Write(t, filepath.Join(out, "tool-"+filepath.Base(name)), string(body))
	}
	cases := []m9Case{
		{name: "local_venv", local: true, known: true},
		{name: "explicit_wins", local: true, explicit: true, known: true},
		{name: "sandbox_canonical", local: true, known: true, sandbox: true},
		{name: "inherited_virtual_env", inherited: true, known: true},
		{name: "unknown", unknown: true},
		{name: "cold_unknown_baseline", unknown: true, cold: true},
		{name: "uv_local_wins", local: true, manager: "uv", known: true, trusted: true},
		{name: "uv_base_rejected", manager: "uv", unknown: true, trusted: true, reason: "environment_manager_not_project_environment"},
		{name: "poetry_external", manager: "poetry", known: true, trusted: true},
		{name: "untrusted_uv", manager: "uv", unknown: true, reason: "environment_manager_untrusted"},
		{name: "untrusted_poetry", manager: "poetry", unknown: true, reason: "environment_manager_untrusted"},
		{name: "missing_manager", manager: "uv", missingManager: true, unknown: true, trusted: true, reason: "environment_manager_unavailable"},
		{name: "manager_failure", manager: "uv", managerFailure: true, unknown: true, trusted: true, reason: "environment_manager_query_failed"},
		{name: "missing_configured_venv", configuredMissing: true},
		{name: "sandbox_relative_venvPath", local: true, known: true, trusted: true, sandbox: true, relativeConfig: true},
	}
	for i, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			p := &m9Probe{t: t, tools: tools, binary: binary, repo: repo, spec: test, dir: filepath.Join(out, fmt.Sprintf("%02d", i))}
			p.run()
		})
	}
}

type m9Case struct {
	name, manager, reason                                                                                                                 string
	local, explicit, inherited, known, unknown, trusted, sandbox, relativeConfig, configuredMissing, missingManager, managerFailure, cold bool
}

type m9Probe struct {
	t                                        *testing.T
	tools, binary, repo, dir, root, selected string
	env                                      []string
	spec                                     m9Case
	wire                                     *probeMCP
	ctx                                      context.Context
	events                                   int
	watch                                    int
	sequence                                 int
	lateSeen                                 bool
	updatesSeen                              bool
	ackedNote                                string
	acknowledgedCursor                       uint64
	lastEditAt                               time.Time
	quietChecks                              int
	progressVetoes                           int
	pullVetoes                               int
	pullVetoChecks                           int
	evidenceSeen                             map[string]bool
	evidenceBatches                          map[string]map[string]any
	before                                   map[string]string
	beforeEnvironment                        map[string]string
	intentionalSource                        map[string]bool
	postFix                                  *m9PostFix
	postFixReplies                           []map[string]any
	postFixCoverage                          map[string]any
	historicalEnd                            time.Time
	historicalCut                            []byte
	historicalFirstCall                      int
	historicalInspected                      int
	historicalCalls                          []m9HistoricalCall
}

func m9RequiredDir(t *testing.T, name string) string {
	t.Helper()
	path := os.Getenv(name)
	if !filepath.IsAbs(path) {
		t.Fatalf("%s must be absolute", name)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || strings.HasPrefix(real+"/", "/tmp/") || real == "/" {
		t.Fatalf("%s must exist outside /tmp: %v", name, err)
	}
	return real
}

func m9Executable(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if !filepath.IsAbs(path) || err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		t.Fatalf("missing executable prerequisite %q: %v", path, err)
	}
}

func m9Write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func m9JSON(t *testing.T, path string, value any) {
	t.Helper()
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	m9Write(t, path, string(body)+"\n")
}

func m9Exec(t *testing.T, parent context.Context, cwd string, env []string, command string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir, cmd.Env, cmd.WaitDelay = cwd, env, time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return unix.Kill(-cmd.Process.Pid, unix.SIGKILL) }
	// Capture descendants before cancellation can detach them from the leader.
	var outputBuffer bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outputBuffer, &outputBuffer
	err := cmd.Start()
	if err == nil {
		stop := m9TrackTree(cmd)
		err = cmd.Wait()
		stop(t)
	}
	output := outputBuffer.Bytes()
	for _, entry := range env {
		if path, ok := strings.CutPrefix(entry, "M9_COMMAND_LOG="); ok {
			log, openErr := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if openErr != nil {
				t.Fatal(openErr)
			}
			_, _ = fmt.Fprintf(log, "%s %q\nerror=%v\n%s\n", command, args, err, output)
			_ = log.Close()
		}
	}
	if err != nil {
		t.Fatalf("fixture/prerequisite command %s %v: %v\n%s", command, args, err, output)
	}
	return strings.TrimSpace(string(output))
}

// Bound both streams from the real manager query without changing its exit.
type m9ManagerOutput struct {
	bytes.Buffer
	truncated bool
}

func (w *m9ManagerOutput) Write(body []byte) (int, error) {
	size := len(body)
	const limit = 64 << 10
	if remaining := limit - w.Len(); size > remaining {
		body = body[:remaining]
		w.truncated = true
	}
	_, _ = w.Buffer.Write(body)
	return size, nil
}

// Prove the negative fixture before Huyang starts. A missing executable,
// cancellation, timeout or signal is a broken prerequisite, not the intended
// normal nonzero result of uv's unavailable-version query.
func (p *m9Probe) verifyManagerFailure() {
	p.t.Helper()
	command, err := filepath.EvalSymlinks(filepath.Join(p.tools, "bin", "uv"))
	if err != nil || !m9Within(p.tools, command) {
		p.t.Fatalf("uv preflight must resolve to the private pinned executable: %v", err)
	}
	m9Executable(p.t, command)
	binary, err := os.ReadFile(command)
	if err != nil {
		p.t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	before := m9Hashes(p.t, p.root)
	inputs := map[string]string{}
	inputHashes := map[string]string{}
	for _, path := range []string{filepath.Join(p.root, ".python-version"), filepath.Join(p.root, "pyproject.toml"),
		filepath.Join(p.root, ".huyang.toml"), filepath.Join(p.dir, "config", "huyang", "config.toml")} {
		body, err := os.ReadFile(path)
		if err != nil {
			p.t.Fatal(err)
		}
		inputs[path] = string(body)
		hash := sha256.Sum256(body)
		inputHashes[path] = hex.EncodeToString(hash[:])
	}
	ctx, cancel := context.WithTimeout(p.ctx, 30*time.Second)
	defer cancel()
	args := []string{"--offline", "python", "find", "--no-python-downloads"}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir, cmd.Env, cmd.WaitDelay = p.root, p.env, time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return unix.Kill(-cmd.Process.Pid, unix.SIGKILL) }
	var stdout, stderr m9ManagerOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Start()
	if err == nil {
		stop := m9TrackTree(cmd)
		err = cmd.Wait()
		stop(p.t)
	}
	exit, exited := err.(*exec.ExitError)
	code := -1
	if exited {
		code = exit.ExitCode()
	}
	outcome := "unexpected_success"
	switch {
	case ctx.Err() != nil:
		outcome = "cancelled_or_timed_out"
	case exited && code > 0:
		outcome = "nonzero_exit"
	case err != nil:
		outcome = "launch_or_abnormal_failure"
	}
	after := m9Hashes(p.t, p.root)
	m9JSON(p.t, filepath.Join(p.dir, "manager-failure-preflight.json"), map[string]any{
		"command": command, "command_sha256": hex.EncodeToString(digest[:]), "args": args, "cwd": p.root,
		"fixture_inputs": inputs, "fixture_input_hashes": inputHashes, "before_project_hashes": before, "after_project_hashes": after,
		"stdout": stdout.String(), "stderr": stderr.String(), "stdout_truncated": stdout.truncated, "stderr_truncated": stderr.truncated,
		"exit_code": code, "outcome": outcome, "error": fmt.Sprint(err), "context_error": fmt.Sprint(ctx.Err()),
	})
	if !maps.Equal(before, after) {
		p.t.Fatal("uv negative-fixture preflight changed canonical fixture bytes")
	}
	if outcome != "nonzero_exit" {
		p.t.Fatalf("manager_failure requires a concrete normal nonzero uv exit, got %s: %v", outcome, err)
	}
}

func (p *m9Probe) venv(path string, dependency bool) string {
	p.t.Helper()
	// CPython records the creator's directory in pyvenv.cfg. The tools/bin
	// alias is not a Python installation prefix; use the resolved base binary.
	m9Exec(p.t, p.ctx, p.root, p.env, p.basePython(), "-I", "-m", "venv", "--without-pip", path)
	if dependency {
		site := filepath.Join(path, "lib", "python3.12", "site-packages")
		m9Write(p.t, filepath.Join(site, "m9_fixture_dep", "__init__.py"), "VALUE: int = 42\n")
		m9Write(p.t, filepath.Join(site, "m9_fixture_dep", "py.typed"), "")
		m9Write(p.t, filepath.Join(site, "m9_fixture_dep-1.0.dist-info", "METADATA"), "Metadata-Version: 2.1\nName: m9-fixture-dep\nVersion: 1.0\n")
	}
	python := filepath.Join(path, "bin", "python")
	p.verifyEnvironment(python, dependency)
	return python
}

func (p *m9Probe) basePython() string {
	p.t.Helper()
	python, err := filepath.EvalSymlinks(filepath.Join(p.tools, "bin", "python"))
	if err != nil || !m9Within(filepath.Join(p.tools, "python"), python) {
		p.t.Fatal("base Python must resolve into the private managed installation")
	}
	return python
}

// Exercise the same non-isolated interpreter path query Pyright uses, as well
// as isolated mode. Executability and --version alone do not prove stdlib or
// venv site-packages are usable. Keep stdout/stderr in fixture-commands.log.
func (p *m9Probe) verifyEnvironment(python string, dependency bool) {
	p.t.Helper()
	root := filepath.Dir(filepath.Dir(python))
	script := `import json, os, sys
assert sys.prefix == sys.argv[1], (sys.prefix, sys.argv[1])
assert sys.prefix != sys.base_prefix, (sys.prefix, sys.base_prefix)
site = os.path.join(sys.prefix, "lib", "python3.12", "site-packages")
assert site in sys.path, sys.path
result = dict(prefix=sys.prefix, base_prefix=sys.base_prefix, path=sys.path, json=json.__file__)
if sys.argv[2] == "true":
    import m9_fixture_dep
    assert m9_fixture_dep.__file__.startswith(site + os.sep), m9_fixture_dep.__file__
    result["dependency"] = m9_fixture_dep.__file__
print(json.dumps(result))
`
	for _, isolated := range []bool{false, true} {
		args := []string{"-c", script, root, strconv.FormatBool(dependency)}
		if isolated {
			args = append([]string{"-I"}, args...)
		}
		m9Exec(p.t, p.ctx, p.root, p.env, python, args...)
	}
}

func (p *m9Probe) fixture() {
	m9Write(p.t, filepath.Join(p.dir, "lsp.jsonl"), "")
	p.root = filepath.Join(p.dir, "root")
	m9Write(p.t, filepath.Join(p.root, ".huyang.toml"), "# Explicit empty pipeline: no automatic project commands.\n")
	m9Write(p.t, filepath.Join(p.root, "pyproject.toml"), "[project]\nname = \"m9-probe\"\nversion = \"1.0.0\"\nrequires-python = \">=3.12,<3.13\"\n")
	bin := filepath.Join(p.dir, "bin")
	for _, name := range []string{"python", "python3", "nvim", "node", "pyright-langserver", "gopls", "git", "sh", "bash", "readlink", "uname", "dirname", "realpath", "uv", "poetry"} {
		if p.spec.missingManager && name == "uv" {
			continue
		}
		if err := os.MkdirAll(bin, 0700); err != nil {
			p.t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(p.tools, "bin", name), filepath.Join(bin, name)); err != nil {
			p.t.Fatal(err)
		}
	}
	p.env = []string{
		"HOME=" + os.Getenv("HOME"), "LANG=C.UTF-8", "PATH=" + bin, "TMPDIR=" + filepath.Join(p.dir, "tmp"),
		"M9_COMMAND_LOG=" + filepath.Join(p.dir, "fixture-commands.log"),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"HUYANG_FRICTION=0", "HUYANG_RUNTIME_PATH=" + p.repo,
		"XDG_CONFIG_HOME=" + filepath.Join(p.dir, "config"), "XDG_DATA_HOME=" + filepath.Join(p.dir, "data"),
		"XDG_CACHE_HOME=" + filepath.Join(p.dir, "cache"), "XDG_STATE_HOME=" + filepath.Join(p.dir, "xdg-state"),
		"XDG_RUNTIME_DIR=" + filepath.Join(p.dir, "run"),
		"UV_CACHE_DIR=" + filepath.Join(p.dir, "uv-cache"), "UV_PYTHON_INSTALL_DIR=" + filepath.Join(p.tools, "python"),
		"UV_OFFLINE=true", "UV_PYTHON_DOWNLOADS=never", "PYTHONDONTWRITEBYTECODE=1", "PIP_NO_INDEX=1",
		"POETRY_CONFIG_DIR=" + filepath.Join(p.dir, "poetry-config"), "POETRY_CACHE_DIR=" + filepath.Join(p.dir, "poetry-cache"),
		"POETRY_DATA_DIR=" + filepath.Join(p.dir, "poetry-data"), "POETRY_NO_INTERACTION=1",
		"POETRY_KEYRING_ENABLED=false", "NETRC=/dev/null",
		"VIRTUALENV_NO_PERIODIC_UPDATE=true", "VIRTUALENV_DOWNLOAD=false",
		"M9_TRACE=" + filepath.Join(p.dir, "lsp.jsonl"), "M9_LSPCONFIG=" + filepath.Join(p.tools, "nvim-lspconfig"),
		"M9_PYRIGHT=" + filepath.Join(bin, "pyright-langserver"),
	}
	for _, name := range []string{"config", "data", "cache", "xdg-state", "poetry-config", "poetry-cache", "poetry-data", "run", "tmp"} {
		if err := os.MkdirAll(filepath.Join(p.dir, name), 0700); err != nil {
			p.t.Fatal(err)
		}
	}
	if p.spec.local {
		p.selected = p.venv(filepath.Join(p.root, ".venv"), !p.spec.explicit)
	}
	if p.spec.explicit {
		p.selected = p.venv(filepath.Join(p.dir, "explicit-env"), true)
		p.env = append(p.env, "M9_EXPLICIT_PYTHON="+p.selected)
	}
	if p.spec.inherited {
		p.selected = p.venv(filepath.Join(p.dir, "inherited-env"), true)
		p.env = append(p.env, "VIRTUAL_ENV="+filepath.Dir(filepath.Dir(p.selected)))
	}
	if p.spec.relativeConfig {
		m9Write(p.t, filepath.Join(p.root, "pyrightconfig.json"), "{\"venvPath\":\".\",\"venv\":\".venv\"}\n")
	}
	if p.spec.configuredMissing {
		m9Write(p.t, filepath.Join(p.root, "pyrightconfig.json"), "{\"venvPath\":\".\",\"venv\":\"missing-env\"}\n")
	}
	if p.spec.manager != "" {
		m9Write(p.t, filepath.Join(p.root, p.spec.manager+".lock"), "")
	}
	if p.spec.manager == "uv" {
		version := "3.12.8"
		if p.spec.managerFailure {
			// uv version components are u8; 999 is parsed as an ignored
			// executable name rather than an unavailable Python version.
			version = "3.99.0"
		}
		m9Write(p.t, filepath.Join(p.root, ".python-version"), version+"\n")
	}
	if p.spec.name == "poetry_external" {
		m9Write(p.t, filepath.Join(p.root, "poetry.toml"), fmt.Sprintf("[virtualenvs]\nin-project = false\npath = %q\n[virtualenvs.options]\nno-pip = true\nalways-copy = false\n", filepath.Join(p.dir, "poetry-envs")))
		poetry := filepath.Join(p.tools, "bin", "poetry")
		m9Exec(p.t, p.ctx, p.root, p.env, poetry, "--no-plugins", "env", "use", p.basePython())
		envRoot := m9Exec(p.t, p.ctx, p.root, p.env, poetry, "--no-plugins", "env", "info", "--path")
		if !strings.HasPrefix(envRoot, filepath.Join(p.dir, "poetry-envs")+string(filepath.Separator)) {
			p.t.Fatal("Poetry escaped its job-local env directory")
		}
		p.selected = filepath.Join(envRoot, "bin", "python")
		if info, err := os.Lstat(p.selected); err != nil || info.Mode()&os.ModeSymlink == 0 {
			p.t.Fatal("external Poetry venv must exercise a symlinked Python")
		}
		m9Write(p.t, filepath.Join(envRoot, "lib", "python3.12", "site-packages", "m9_fixture_dep", "__init__.py"), "VALUE: int = 42\n")
		m9Write(p.t, filepath.Join(envRoot, "lib", "python3.12", "site-packages", "m9_fixture_dep", "py.typed"), "")
		p.verifyEnvironment(p.selected, true)
	}
	imports := "import m9_fixture_dep\n"
	if p.spec.unknown {
		imports += "import m9_absent_one\nimport m9_absent_two\n"
	}
	m9Write(p.t, filepath.Join(p.root, "main.py"), imports+"value: int = 1\n# marker one\n")
	trust := "[]"
	if p.spec.trusted || p.spec.sandbox {
		trust = "[" + strconv.Quote(p.root) + "]"
	}
	m9Write(p.t, filepath.Join(p.dir, "config", "huyang", "config.toml"), "[trust]\nroots = "+trust+"\n")
	m9Write(p.t, filepath.Join(p.dir, "init.lua"), m9NeovimInit)
	p.env = append(p.env, "HUYANG_HEADLESS_INIT="+filepath.Join(p.dir, "init.lua"))
	m9JSON(p.t, filepath.Join(p.dir, "fixture-preparation-hashes.json"), m9Hashes(p.t, p.root))
	if p.spec.managerFailure {
		p.verifyManagerFailure()
	}
	m9Exec(p.t, p.ctx, p.root, p.env, filepath.Join(bin, "git"), "init", "-q")
	p.before = m9Hashes(p.t, p.root)
	m9JSON(p.t, filepath.Join(p.dir, "before-hashes.json"), p.before)
	if p.selected != "" {
		p.beforeEnvironment = m9Hashes(p.t, filepath.Dir(filepath.Dir(p.selected)))
		m9JSON(p.t, filepath.Join(p.dir, "before-environment-hashes.json"), p.beforeEnvironment)
	}
}

const m9NeovimInit = `
vim.opt.runtimepath:prepend(vim.env.M9_LSPCONFIG)
vim.opt.swapfile = false
vim.opt.undofile = false
local trace_sequence = 0
local function trace(event)
    trace_sequence = trace_sequence + 1
    event.trace_seq, event.process_id = trace_sequence, vim.fn.getpid()
    local sec, usec = vim.uv.gettimeofday()
    if type(sec) == "table" then usec = sec.usec; sec = sec.sec end
    event.unix_us = sec * 1000000 + usec
    local f = assert(io.open(vim.env.M9_TRACE, "a"))
    f:write(vim.json.encode(event), "\n")
    f:close()
    return trace_sequence
end
local observer = require("huyang.diagnostic_observer")
local collection_id, active_collection = 0, nil
observer.install(function(event)
    event.kind, event.collection_id = "diagnostic_observation", active_collection
    trace(event)
end)
trace({kind="observer_ready"})
local notify = vim.rpcnotify
vim.rpcnotify = function(channel, method, ...)
    if method == "huyang/result" then
        local args = {...}
        if type(args[2]) == "table" then observer.transport(args[1], args[2]) end
    end
    return notify(channel, method, ...)
end
local edit = require("huyang.edit")
local late_evidence = edit.late_evidence
edit.late_evidence = function(...)
    collection_id = collection_id + 1
    local previous = active_collection
    active_collection = collection_id
    trace({kind="collection_begin", collection_id=active_collection})
    local result = late_evidence(...)
    trace({kind="collection_end", collection_id=active_collection, observer_status=observer.status()})
    active_collection = previous
    return result
end
local publish = vim.lsp.handlers["textDocument/publishDiagnostics"] or vim.lsp.diagnostic.on_publish_diagnostics
local function observe_publish(err, params, ctx, config)
    trace({kind="publish", params=params, client_id=ctx.client_id, err=err})
    return publish(err, params, ctx, config)
end
-- These are LOCAL observations, never producer document-version proof.
local function snapshot(client, bufnr, params)
    local uri = params and params.textDocument and params.textDocument.uri
    if not bufnr and uri then bufnr = vim.fn.bufnr(vim.uri_to_fname(uri)) end
    if not bufnr or not vim.api.nvim_buf_is_loaded(bufnr) then return {} end
    local path = vim.uv.fs_realpath(vim.api.nvim_buf_get_name(bufnr))
    local body = table.concat(vim.api.nvim_buf_get_lines(bufnr, 0, -1, false), "\n")
    if vim.bo[bufnr].endofline then body = body .. "\n" end
    local f = path and io.open(path, "rb")
    local disk = f and f:read("*a")
    if f then f:close() end
    return {path=path, uri=uri, bufnr=bufnr, changedtick=vim.api.nvim_buf_get_changedtick(bufnr),
        local_version=vim.lsp.util.buf_versions[bufnr], sha256=vim.fn.sha256(body),
        disk_sha256=disk and vim.fn.sha256(disk),
        client_current=vim.lsp.get_client_by_id(client.id) == client,
        attached=not not client.attached_buffers[bufnr]}
end
local function observe_client(client)
    -- Preserve every request, callback, error, result and cancellation unchanged.
    local request, cancel = client.request, client.cancel_request
    local sequence, ids = 0, {}
    client.request = function(self, method, params, handler, bufnr)
        if method ~= "textDocument/diagnostic" then
            return request(self, method, params, handler, bufnr)
        end
        sequence = sequence + 1
        local id = tostring(vim.fn.getpid()) .. ":" .. self.id .. ":" .. sequence
        local dispatch = snapshot(self, bufnr, params)
        trace({kind="pull_request", request_id=id, request_params=params, client_id=self.id, snapshot=dispatch})
        local inner = handler or self.handlers[method] or vim.lsp.handlers[method]
        if inner then
            handler = function(err, result, ctx, config)
                local callback_seq = trace({kind="pull", request_id=id, params=result, request_params=params,
                    client_id=self.id, callback_client_id=ctx and ctx.client_id, err=err,
                    snapshot=snapshot(self, bufnr, params)})
                return observer.with_raw({request_id=id, callback_seq=callback_seq,
                    process_id=vim.fn.getpid(), client_id=self.id}, inner, err, result, ctx, config)
            end
        end
        local sent, rpc_id = request(self, method, params, handler, bufnr)
        if sent and rpc_id then ids[rpc_id] = id end
        trace({kind="pull_dispatch", request_id=id, client_id=self.id, sent=sent, rpc_id=rpc_id})
        return sent, rpc_id
    end
    client.cancel_request = function(self, rpc_id)
        if ids[rpc_id] then
            trace({kind="pull_cancel", request_id=ids[rpc_id], client_id=self.id, rpc_id=rpc_id})
        end
        return cancel(self, rpc_id)
    end
end
local settings = { python = { analysis = { typeCheckingMode = "basic", diagnosticMode = "openFilesOnly" } } }
if vim.env.M9_EXPLICIT_PYTHON then settings.python.pythonPath = vim.env.M9_EXPLICIT_PYTHON end
vim.lsp.config("pyright", { cmd = {vim.env.M9_PYRIGHT, "--stdio"}, filetypes = {"python"},
    root_markers = {"pyproject.toml", ".git"}, settings = settings, on_init = observe_client,
    handlers = { ["textDocument/publishDiagnostics"] = observe_publish } })
vim.lsp.enable("pyright")
vim.api.nvim_create_autocmd("LspAttach", {callback = function(ev)
    local client = vim.lsp.get_client_by_id(ev.data.client_id)
    trace({kind="attach", client_id=client.id, name=client.name,
        path=vim.api.nvim_buf_get_name(ev.buf), root=type(client.config.root_dir) == "string" and client.config.root_dir or "<callback>",
        settings=client.config.settings})
end})
vim.api.nvim_create_autocmd("LspProgress", {callback = function(ev)
    trace({kind="progress", data=ev.data})
end})
`

func (p *m9Probe) start() {
	socket := filepath.Join(p.dir, "s")
	if len(socket) > 100 {
		p.t.Fatal("private socket path exceeds 100 bytes; shorten HUYANG_M9_RUN")
	}
	log, err := os.Create(filepath.Join(p.dir, "service.log"))
	if err != nil {
		p.t.Fatal(err)
	}
	command := exec.CommandContext(p.ctx, filepath.Join(p.tools, "bin", "strace"), "--kill-on-exit", "--seccomp-bpf", "-ff", "-e", "trace=execve,execveat", "-s", "2048", "-o", filepath.Join(p.dir, "exec"), p.binary, "serve", "--socket", socket, "--state-dir", filepath.Join(p.dir, "state"))
	command.Env, command.Stdout, command.Stderr, command.WaitDelay = p.env, log, log, time.Second
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		return unix.Kill(-command.Process.Pid, unix.SIGTERM)
	}
	if err := command.Start(); err != nil {
		p.t.Fatal(err)
	}
	stop := m9TrackTree(command)
	done := make(chan struct{})
	go func() { _ = command.Wait(); close(done) }()
	p.t.Cleanup(func() {
		stop(p.t) // Waits for group AND tracked descendants, even if strace exited.
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			p.t.Error("strace leader was not reaped after tree termination")
		}
		_ = log.Close()
		p.assertManagerTrace() // Trace is final only after the entire tree is gone.
	})
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		select {
		case <-done:
			p.t.Fatal("private service exited before readiness; see service.log")
		default:
		}
		p.event()
		if p.ctx.Err() != nil {
			p.t.Fatal("private service socket deadline exceeded")
		}
	}
	_, _ = unix.InotifyRmWatch(p.events, uint32(p.watch))
	p.watch, err = unix.InotifyAddWatch(p.events, filepath.Join(p.dir, "lsp.jsonl"), unix.IN_MODIFY|unix.IN_CLOSE_WRITE)
	if err != nil {
		p.t.Fatal(err)
	}
	p.wire = p.connect(socket, "json")
}

func (p *m9Probe) connect(socket, style string) *probeMCP {
	cmd := exec.CommandContext(p.ctx, p.binary, "mcp", "--socket", socket, "--profile", "experimental", "--reply-style", style)
	cmd.Env, cmd.WaitDelay = p.env, time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return unix.Kill(-cmd.Process.Pid, unix.SIGKILL) }
	in, err := cmd.StdinPipe()
	if err != nil {
		p.t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		p.t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(p.dir, "wire-"+style+".jsonl"))
	if err != nil {
		p.t.Fatal(err)
	}
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		p.t.Fatal(err)
	}
	stop := m9TrackTree(cmd)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	p.t.Cleanup(func() {
		_ = in.Close()
		stop(p.t)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			p.t.Error("adapter leader was not reaped")
		}
		_ = log.Close()
	})
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), 32<<20)
	wire := &probeMCP{in: in, out: scanner, log: log}
	wire.rpc(p.t, "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "m9-python-probe", "version": "1"}})
	_, _ = io.WriteString(in, "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n")
	return wire
}

// inotify wakes on real daemon/LSP artifact events. The bounded 200ms quiet
// observation timeout also permits Huyang's 150ms empty-publication proof;
// there is no fixture warmup sleep or fixed post-edit delay.
func (p *m9Probe) event() {
	ms := 200
	if deadline, ok := p.ctx.Deadline(); ok {
		if left := time.Until(deadline).Milliseconds(); left < int64(ms) {
			ms = int(left)
		}
	}
	if ms <= 0 {
		return
	}
	fds := []unix.PollFd{{Fd: int32(p.events), Events: unix.POLLIN}}
	_, _ = unix.Poll(fds, ms)
	buffer := make([]byte, 16384)
	_, _ = unix.Read(p.events, buffer)
}

func (p *m9Probe) call(name string, args map[string]any) map[string]any {
	p.t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	args["root"] = p.root
	// Full bodies recover current findings, not acknowledged notice history.
	// Keep the consumed floor so intermediate transitions remain replayable;
	// a newer report cursor alone must never acknowledge unchecked notices.
	if name == "diagnostics" && args["full"] == true && p.acknowledgedCursor > 0 {
		if _, explicit := args["since"]; !explicit {
			args["since"] = fmt.Sprintf("diagcur_%d", p.acknowledgedCursor)
		}
	}
	var historicalBefore int
	if p.postFix != nil {
		body, err := os.ReadFile(filepath.Join(p.dir, "lsp.jsonl"))
		if err != nil {
			p.t.Fatal(err)
		}
		historicalBefore = len(body)
	}
	reply := p.wire.call(p.t, name, args)
	if p.postFix != nil {
		body, err := os.ReadFile(filepath.Join(p.dir, "lsp.jsonl"))
		if err != nil {
			p.t.Fatal(err)
		}
		p.historicalCalls = append(p.historicalCalls, m9HistoricalCall{Name: name, Reply: reply, Before: historicalBefore, After: len(body)})
	}
	if name == "diagnostics" && reply["code"] == nil && m9Report(reply) != nil {
		switch reply["outcome"] {
		case "ok", "provisional", "unavailable":
			since, _ := args["since"].(string)
			var acknowledged uint64
			if _, err := fmt.Sscanf(since, "diagcur_%d", &acknowledged); err == nil && acknowledged > p.acknowledgedCursor {
				p.acknowledgedCursor = acknowledged
			}
		}
	}
	p.sequence++
	m9JSON(p.t, filepath.Join(p.dir, fmt.Sprintf("%03d-%s.json", p.sequence, name)), reply)
	if p.ackedNote != "" {
		for _, item := range append(probeFindings(reply, "main.py", "new"), probeUpdates(reply, "main.py", "new")...) {
			if item["id"] == p.ackedNote {
				p.t.Fatal("acknowledged stable environment note reannounced as new")
			}
		}
	}
	if name == "diagnostics" && len(probeSlice(reply["diagnostic_updates"])) > 0 {
		p.updatesSeen = true
	}
	if name == "diagnostics" && p.postFix == nil && len(probeSlice(m9Object(reply["evidence"])["ids"])) > 0 {
		p.inspectEvidence(reply)
	}
	if p.postFix != nil {
		p.postFixReplies = append(p.postFixReplies, reply)
	}
	return reply
}

func m9Object(value any) map[string]any { object, _ := value.(map[string]any); return object }
func m9Text(value any) string           { text, _ := value.(string); return text }
func m9Report(reply map[string]any) map[string]any {
	return m9Object(m9Object(reply["data"])["diagnostics"])
}
func m9Finding(item map[string]any) map[string]any {
	if value := m9Object(item["finding"]); value != nil {
		return value
	}
	return item
}
func m9CodeCount(items []map[string]any, code string) int {
	n := 0
	for _, item := range items {
		if m9Finding(item)["code"] == code {
			n++
		}
	}
	return n
}
func m9TypeCount(items []map[string]any) int {
	n := 0
	for _, item := range items {
		code := m9Text(m9Finding(item)["code"])
		if code == "reportAssignmentType" || code == "reportGeneralTypeIssues" {
			n++
		}
	}
	return n
}

func (p *m9Probe) collect() map[string]any {
	p.t.Helper()
	for p.ctx.Err() == nil {
		reply := p.call("diagnostics", map[string]any{"full": true})
		items := probeFindings(reply, "main.py", "current")
		if m9TypeCount(items) > 0 {
			if p.spec.unknown {
				if m9CodeCount(items, "python_environment_unresolved") == 1 && m9Report(reply)["confidence"] == "provisional" {
					return reply
				}
			} else if p.spec.configuredMissing {
				if m9CodeCount(items, "reportMissingImports") > 0 {
					return reply
				}
			} else if m9CodeCount(items, "reportMissingImports") == 0 && m9CodeCount(items, "python_environment_unresolved") == 0 {
				return reply
			}
		}
		p.event()
	}
	p.t.Fatal("deadline: expected real Python diagnostics never arrived (see per-call, LSP and process artifacts)")
	return nil
}

type m9PostFix struct {
	Path, Hash, Revision, Transaction string
	StartedUS                         float64
	IDs                               []string
}

func (f *m9PostFix) uri() string { return (&url.URL{Scheme: "file", Path: f.Path}).String() }

type m9PullObservation struct {
	Request, Response map[string]any
}

type m9TraceState struct {
	Candidate                      *m9PullObservation
	Pushes                         []map[string]any
	Provider                       string
	ProducerProof, UnversionedPush bool
	Pending                        bool
	ProgressBegins, Epoch          int
	LastSeq                        float64
	ProofBarrier                   float64
	Reason                         string
}

// Local identity binds an observation to bytes we requested, never to the
// producer's analyzed version. Both snapshots must agree with the receipt hash.
func m9CorrelatedEmpty(request, response map[string]any, fixed *m9PostFix) error {
	if request == nil || m9Text(request["request_id"]) == "" || request["request_id"] != response["request_id"] ||
		request["client_id"] != response["client_id"] || request["process_id"] != response["process_id"] ||
		response["callback_client_id"] != request["client_id"] {
		return fmt.Errorf("request/callback client identity is uncorrelated")
	}
	start, sok := request["unix_us"].(float64)
	finish, fok := response["unix_us"].(float64)
	if !sok || !fok || start < fixed.StartedUS || finish < start {
		return fmt.Errorf("request predates the fix or lacks dispatch/completion time")
	}
	if response["err"] != nil {
		return fmt.Errorf("pull callback returned an error/cancellation")
	}
	result := m9Object(response["params"])
	items, array := result["items"].([]any)
	if result["kind"] != "full" || !array || len(items) != 0 {
		return fmt.Errorf("pull is not an explicit successful full items=[] response")
	}
	a, b := m9Object(request["snapshot"]), m9Object(response["snapshot"])
	for _, event := range []map[string]any{request, response} {
		uri := m9Text(m9Object(m9Object(event["request_params"])["textDocument"])["uri"])
		if uri != fixed.uri() {
			return fmt.Errorf("request URI is not the exact canonical fixed document")
		}
	}
	for _, snapshot := range []map[string]any{a, b} {
		version, vok := snapshot["local_version"].(float64)
		tick, tok := snapshot["changedtick"].(float64)
		buffer, bok := snapshot["bufnr"].(float64)
		if snapshot["path"] != fixed.Path || snapshot["uri"] != fixed.uri() ||
			snapshot["sha256"] != fixed.Hash || snapshot["disk_sha256"] != fixed.Hash ||
			snapshot["client_current"] != true || snapshot["attached"] != true ||
			!vok || version < 0 || !tok || tick < 1 || !bok || buffer < 1 {
			return fmt.Errorf("local postimage/client is not synchronized at dispatch and completion")
		}
	}
	for _, key := range []string{"bufnr", "local_version", "changedtick", "sha256", "disk_sha256", "path", "uri"} {
		if a[key] == nil || a[key] != b[key] {
			return fmt.Errorf("local identity changed during request: %s", key)
		}
	}
	return nil
}

// Replay the complete trace, tracking real progress tokens for the selected
// client, including work begun before the fix. No count or quiet duration proves
// analysis completion. Epoch changes force another event-driven observation.
func m9PostFixTrace(events []map[string]any, fixed *m9PostFix) (m9TraceState, error) {
	state := m9TraceState{Reason: "no qualifying post-fix request/callback"}
	var client, process any
	for _, event := range events {
		if event["kind"] != "attach" || event["path"] != fixed.Path {
			continue
		}
		if event["name"] != "pyright" || event["client_id"] == nil || event["process_id"] == nil {
			return state, fmt.Errorf("canonical attachment has no unambiguous Pyright identity")
		}
		if client != nil && (client != event["client_id"] || process != event["process_id"]) {
			return state, fmt.Errorf("multiple canonical clients/processes: unsupported mixed transport")
		}
		client, process = event["client_id"], event["process_id"]
	}
	if client == nil {
		return state, fmt.Errorf("canonical client attachment not observed")
	}
	state.Provider = fmt.Sprintf("pyright#%v", client)
	requests, sent, cancelled := map[string]map[string]any{}, map[string]bool{}, map[string]bool{}
	for _, event := range events {
		if event["process_id"] != process || event["client_id"] != client {
			continue
		}
		id := m9Text(event["request_id"])
		switch event["kind"] {
		case "pull_request":
			requests[id] = event
		case "pull_dispatch":
			rpcID, valid := event["rpc_id"].(float64)
			sent[id] = event["sent"] == true && valid && rpcID >= 1
		case "pull_cancel":
			cancelled[id] = true
		}
	}
	pending, inflight := map[string]bool{}, map[string]bool{}
	var barrier float64
	invalidate := func(reason string) {
		state.Candidate, state.Reason = nil, reason
		state.Epoch++
	}
	for _, event := range events {
		eventURI := m9Object(event["params"])["uri"]
		if event["kind"] == "pull" || event["kind"] == "pull_request" {
			eventURI = m9Object(m9Object(event["request_params"])["textDocument"])["uri"]
		}
		if eventURI == fixed.uri() && (event["process_id"] != process || event["client_id"] != client) {
			return state, fmt.Errorf("canonical transport came from another client/process")
		}
		if event["process_id"] != process {
			continue
		}
		seq, seqOK := event["trace_seq"].(float64)
		if !seqOK || seq <= state.LastSeq {
			return state, fmt.Errorf("trace sequence is missing or unordered")
		}
		state.LastSeq = seq
		if event["kind"] == "progress" {
			data := m9Object(event["data"])
			if data["client_id"] == nil {
				return state, fmt.Errorf("progress lacks client identity")
			}
			if data["client_id"] != client {
				continue
			}
			params := m9Object(data["params"])
			switch params["token"].(type) {
			case string, float64:
			default:
				return state, fmt.Errorf("selected-client progress lacks a valid token")
			}
			state.ProofBarrier = seq
			token, _ := json.Marshal(params["token"])
			switch m9Object(params["value"])["kind"] {
			case "begin", "report":
				pending[string(token)] = true
				if m9Object(params["value"])["kind"] == "begin" {
					state.ProgressBegins++
				}
				invalidate("known client progress began or continued after candidate")
			case "end":
				delete(pending, string(token))
				barrier = seq
				invalidate("require a fresh request after known work ends")
			default:
				return state, fmt.Errorf("malformed selected-client progress")
			}
			continue
		}
		if event["client_id"] != client {
			continue
		}
		params := m9Object(event["params"])
		if event["kind"] == "publish" && params["uri"] == fixed.uri() {
			state.Pushes = append(state.Pushes, event)
			state.ProofBarrier = seq
			if _, stamped := params["version"].(float64); stamped {
				state.ProducerProof = true
			} else {
				state.UnversionedPush = true
			}
			invalidate("push observed: pure-pull fallback unavailable")
			continue
		}
		id := m9Text(event["request_id"])
		request := requests[id]
		uri := m9Object(m9Object(event["request_params"])["textDocument"])["uri"]
		if event["kind"] == "pull_cancel" {
			delete(inflight, id)
			if state.Candidate != nil && state.Candidate.Request["request_id"] == id {
				invalidate("candidate request was cancelled")
			}
			continue
		}
		requestURI := m9Object(m9Object(request["request_params"])["textDocument"])["uri"]
		if uri != fixed.uri() && requestURI != fixed.uri() {
			continue
		}
		if event["kind"] == "pull_request" && sent[id] && !cancelled[id] {
			inflight[id] = true
		}
		if event["kind"] != "pull" {
			continue
		}
		delete(inflight, id)
		// Every newer selected-document callback can contradict a candidate,
		// even a stale in-flight nonempty response or an unchanged/error body.
		if err := m9CorrelatedEmpty(request, event, fixed); err != nil || !sent[id] || cancelled[id] {
			invalidate(fmt.Sprintf("nonqualifying callback %s: %v (sent=%v cancelled=%v)", id, err, sent[id], cancelled[id]))
			barrier, state.ProofBarrier = seq, seq
			continue
		}
		dispatchSeq, _ := request["trace_seq"].(float64)
		if len(pending) != 0 || dispatchSeq <= barrier {
			invalidate("empty pull dispatched before known work/contradiction ended")
			continue
		}
		if state.Candidate == nil {
			state.Candidate = &m9PullObservation{Request: request, Response: event}
		}
		state.Reason = "post-fix empty pull observed; clean unproven"
	}
	state.Pending = len(pending) != 0 || len(inflight) != 0
	return state, nil
}

func (p *m9Probe) postFixTrace() (m9TraceState, string) {
	body, err := os.ReadFile(filepath.Join(p.dir, "lsp.jsonl"))
	if err != nil {
		p.t.Fatal(err)
	}
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) != nil {
			p.t.Fatal("malformed/incomplete raw LSP telemetry; cannot assert post-fix observation")
		}
		events = append(events, event)
	}
	state, err := m9PostFixTrace(events, p.postFix)
	if err != nil {
		p.t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	return state, hex.EncodeToString(digest[:])
}

func m9CitedBatches(reply map[string]any) map[string]bool {
	ids := map[string]bool{}
	for _, raw := range m9Object(m9Report(reply)["coverage"]) {
		for _, id := range probeSlice(m9Object(raw)["evidence_ids"]) {
			ids[m9Text(id)] = true
		}
	}
	return ids
}

// Match the selected ledger evidence to the receipt AND the observed request.
// resultId is only an additional correlation field, never version proof.
func m9PostFixBatch(batch map[string]any, fixed *m9PostFix, state m9TraceState, push bool) bool {
	document := m9Text(batch["document"])
	if document != fixed.Path && document != "main.py" {
		return false
	}
	if batch["selected"] != true || batch["provider_id"] != state.Provider ||
		batch["document_revision"] != fixed.Revision || batch["transaction_id"] != fixed.Transaction ||
		fixed.Transaction == "" || batch["staged"] == true || batch["timed_out"] == true ||
		batch["progress_pending"] == true || batch["dimension"] != "edited_documents" {
		return false
	}
	if push {
		if batch["kind"] != "lsp_push" || !m9EffectiveProof(batch) {
			return false
		}
		observed, err := time.Parse(time.RFC3339Nano, m9Text(batch["observed_at"]))
		for _, event := range state.Pushes {
			params := m9Object(event["params"])
			at, _ := event["unix_us"].(float64)
			seq, _ := event["trace_seq"].(float64)
			items, array := params["diagnostics"].([]any)
			if err == nil && seq >= state.ProofBarrier && event["err"] == nil && array && len(items) == len(probeSlice(batch["findings"])) &&
				params["version"] == batch["document_version"] && at >= fixed.StartedUS &&
				at <= float64(observed.UnixMicro()) {
				return true
			}
		}
		return false
	}
	if state.Candidate == nil || len(probeSlice(batch["findings"])) != 0 || batch["kind"] != "lsp_pull" || batch["complete"] != false || batch["document_version"] != nil {
		return false
	}
	response := state.Candidate.Response
	snapshot := m9Object(state.Candidate.Request["snapshot"])
	observed, err := time.Parse(time.RFC3339Nano, m9Text(batch["observed_at"]))
	callbackUS, _ := response["unix_us"].(float64)
	// nil/error/timeout/non-full conversions have no result_id. Without a
	// real response identity we cannot distinguish those batches honestly.
	resultID := m9Text(m9Object(response["params"])["resultId"])
	return resultID != "" && err == nil && float64(observed.UnixMicro()) >= callbackUS &&
		batch["expected_version"] == snapshot["local_version"] &&
		m9Text(batch["result_id"]) == resultID
}

// Check all surfaces, including compact edit deltas and once-only updates. A
// later provisional full report cannot excuse an earlier resolved notice.
func m9PostFixLedger(reply map[string]any, fixed *m9PostFix, pure bool) (map[string]bool, error) {
	resolved := map[string]bool{}
	var walk func(any) error
	walk = func(value any) error {
		switch value := value.(type) {
		case []any:
			for _, item := range value {
				if err := walk(item); err != nil {
					return err
				}
			}
		case map[string]any:
			if value["notices_truncated"] == true || value["diagnostic_updates_truncated"] == true {
				return fmt.Errorf("diagnostic transitions were truncated")
			}
			if pure && value["confidence"] != nil && value["confidence"] != "provisional" {
				return fmt.Errorf("pure-pull intermediate/final report is not provisional")
			}
			if value["kind"] == "resolved" || value["status"] == "resolved" {
				resolved[m9Text(value["id"])] = true
				if pure {
					return fmt.Errorf("pure-pull omission emitted a resolved transition")
				}
			}
			for key, child := range value {
				if key == "resolved" || key == "resolved_ids" {
					for _, raw := range probeSlice(child) {
						id := m9Text(raw)
						if id == "" {
							id = m9Text(m9Object(raw)["id"])
						}
						resolved[id] = true
						if pure {
							return fmt.Errorf("pure-pull omission emitted resolved bodies/IDs")
						}
					}
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	data := m9Object(reply["data"])
	for _, value := range []any{data["diagnostics"], data["diagnostic_delta"], reply["diagnostic_updates"]} {
		if err := walk(value); err != nil {
			return resolved, err
		}
	}
	if reply["diagnostic_updates_truncated"] == true {
		return resolved, fmt.Errorf("diagnostic updates truncated")
	}
	report := m9Report(reply)
	if pure && report != nil {
		retained := map[string]bool{}
		for _, group := range []string{"current", "stale"} {
			for _, raw := range probeSlice(report[group]) {
				item := m9Object(raw)
				if item["status"] == group {
					retained[m9Text(item["id"])] = true
				}
			}
		}
		for _, id := range fixed.IDs {
			if !retained[id] {
				return resolved, fmt.Errorf("pure-pull omitted original current/stale finding %s", id)
			}
		}
	}
	return resolved, nil
}

func (p *m9Probe) collectPostFixTerminal() {
	p.t.Helper()
	inspected := 0
	for p.ctx.Err() == nil {
		reply := p.call("diagnostics", map[string]any{"full": true})
		if m9Report(reply) == nil {
			p.t.Fatal("post-fix full diagnostics omitted the report")
		}
		// Recover every iteration, including the fixing edit and intermediate
		// replies: authority/veto checks must not inspect only the terminal call.
		for inspected < len(p.postFixReplies) {
			item := p.postFixReplies[inspected]
			inspected++
			if len(probeSlice(m9Object(item["evidence"])["ids"])) > 0 && item["data"] != nil &&
				(m9Report(item) != nil || m9Object(m9Object(item["data"])["evidence"]) == nil) {
				p.inspectEvidence(item)
			}
		}
		state, traceHash := p.postFixTrace()
		// A diagnostics call wakes the existing pull watch. Let that actual
		// request/progress settle through trace events before issuing another
		// call; otherwise each poll could keep its replacement in flight.
		for state.Pending && p.ctx.Err() == nil {
			p.event()
			state, traceHash = p.postFixTrace()
		}
		p.postFixCoverage["reached_gate"] = "transport_and_postimage_observation"
		p.postFixCoverage["selected_provider"] = state.Provider
		p.postFixCoverage["producer_proof_observed"] = state.ProducerProof
		p.postFixCoverage["unversioned_push_observed"] = state.UnversionedPush
		p.postFixCoverage["observed_progress_begins"] = state.ProgressBegins
		p.postFixCoverage["known_work_pending"] = state.Pending
		p.postFixCoverage["last_observation_reason"] = state.Reason
		p.postFixCoverage["final_trace_sequence"] = state.LastSeq
		p.postFixCoverage["final_trace_sha256"] = traceHash
		if p.ctx.Err() != nil {
			break
		}
		if state.UnversionedPush {
			p.t.Fatal("unversioned/mixed push transport is outside the strict pure-pull fallback")
		}
		// Recovered producer evidence must agree with the fixing receipt transaction.
		for _, batch := range p.evidenceBatches {
			if batch["provider_id"] != state.Provider || batch["document_revision"] != p.postFix.Revision ||
				(batch["document"] != p.postFix.Path && batch["document"] != "main.py") || batch["selected"] != true {
				continue
			}
			tx := m9Text(batch["transaction_id"])
			if tx != "" {
				if p.postFix.Transaction != "" && tx != p.postFix.Transaction {
					p.t.Fatal("ambiguous fixed-document transaction identity")
				}
				p.postFix.Transaction = tx
			}
		}
		p.postFixCoverage["fixed_transaction"] = p.postFix.Transaction
		resolved := map[string]bool{}
		authorized := false
		proofAtReply := state
		proofAtReply.ProofBarrier = 0 // Later work cannot invalidate a legitimate earlier transition.
		for _, item := range p.postFixReplies {
			// A later stamped push cannot excuse an earlier false authority or
			// resolved notice. Proof must have been cited by this or a prior reply.
			for _, raw := range probeSlice(m9Object(item["evidence"])["ids"]) {
				if state.ProducerProof && m9PostFixBatch(p.evidenceBatches[m9Text(raw)], p.postFix, proofAtReply, true) {
					authorized = true
				}
			}
			ids, err := m9PostFixLedger(item, p.postFix, !authorized)
			if err != nil {
				p.t.Fatal(err)
			}
			maps.Copy(resolved, ids)
		}
		provenResolution := true
		for _, id := range p.postFix.IDs {
			provenResolution = provenResolution && resolved[id]
		}
		var qualifying []string
		for id := range m9CitedBatches(reply) {
			if len(probeSlice(p.evidenceBatches[id]["findings"])) == 0 && m9PostFixBatch(p.evidenceBatches[id], p.postFix, state, state.ProducerProof) {
				qualifying = append(qualifying, id)
			}
		}
		if state.Pending || len(qualifying) == 0 {
			p.postFixCoverage["evidence_gate_limitation"] = "No current selected receipt/transaction-bound batch matches a real identified raw response; nil/error/timeout/non-full conversions cannot qualify."
			p.event()
			continue
		}
		delete(p.postFixCoverage, "evidence_gate_limitation")
		p.postFixCoverage["qualifying_evidence_ids"] = qualifying
		if state.ProducerProof {
			if m9Report(reply)["confidence"] == "authoritative" &&
				len(probeSlice(m9Report(reply)["current"])) == 0 && len(probeSlice(m9Report(reply)["stale"])) == 0 && provenResolution {
				p.postFixCoverage["outcome"] = "authoritative_clean"
				p.postFixCoverage["clean_resolution_proven"] = true
				p.postFixCoverage["no_resolution"] = false
				var pushSequences []any
				for _, event := range state.Pushes {
					params := m9Object(event["params"])
					at, _ := event["unix_us"].(float64)
					for _, id := range qualifying {
						if at >= p.postFix.StartedUS && params["version"] == p.evidenceBatches[id]["document_version"] {
							pushSequences = append(pushSequences, event["trace_seq"])
						}
					}
				}
				p.postFixCoverage["qualifying_push_trace_sequences"] = pushSequences
				p.postFixCoverage["resolved_finding_ids"] = p.postFix.IDs
				p.postFixCoverage["reached_gate"] = "authoritative_clean"
				return
			}
		} else {
			p.t.Fatal("authoritative lane lost its observed stamped producer path")
		}
		p.event()
	}
	p.t.Fatalf("post-fix observation deadline (never success): %+v", p.postFixCoverage)
}

func (p *m9Probe) edit(old, new string) map[string]any {
	body, err := os.ReadFile(filepath.Join(p.root, "main.py"))
	if err != nil || !p.intentionalSource[string(body)] || strings.Count(string(body), old) != 1 {
		p.t.Fatal("intentional edit does not match the expected fixture source")
	}
	// Register before dispatch: a deadline may close MCP after canonical commit.
	p.intentionalSource[strings.Replace(string(body), old, new, 1)] = true
	reply := p.call("edit_apply", map[string]any{"format": false, "operation": map[string]any{"kind": "replace_literal", "path": "main.py", "old": old, "new": new}})
	p.lastEditAt = time.Now()
	data := m9Object(reply["data"])
	if data["canonical_changed"] != true || !strings.HasPrefix(m9Text(data["revision"]), "wsrev_") {
		p.t.Fatalf("edit lacks canonical/revision guard: %+v", reply)
	}
	if len(m9Text(data["document_revision"])) == 0 && len(m9Object(data["document_revisions"])) == 0 {
		p.t.Fatal("edit omitted document guard")
	}
	if len(probeSlice(m9Object(reply["evidence"])["ids"])) > 0 {
		p.inspectEvidence(reply)
	}
	return reply
}

func (p *m9Probe) run() {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	p.ctx = ctx
	defer cancel()
	p.postFixCoverage = map[string]any{"outcome": "unreached", "reached_gate": "positive_environment_gates", "producer_proof_observed": false, "no_resolution": nil, "clean_resolution_proven": false,
		"progress_limitation": "Known client progress and contradictory responses are guarded; future progress cannot be excluded by finite observation."}
	if !p.spec.known {
		p.postFixCoverage["outcome"] = "not_applicable"
	}
	p.evidenceSeen = map[string]bool{}
	p.evidenceBatches = map[string]map[string]any{}
	if err := os.MkdirAll(p.dir, 0700); err != nil {
		p.t.Fatal(err)
	}
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		p.t.Fatal(err)
	}
	p.events = fd
	p.watch, err = unix.InotifyAddWatch(fd, p.dir, unix.IN_CREATE|unix.IN_MODIFY|unix.IN_CLOSE_WRITE)
	if err != nil {
		p.t.Fatal(err)
	}
	defer unix.Close(fd)
	p.t.Cleanup(func() {
		if p.root == "" {
			return
		}
		after := m9Hashes(p.t, p.root)
		m9JSON(p.t, filepath.Join(p.dir, "after-hashes.json"), after)
		if p.before != nil && !maps.Equal(p.before, after) {
			p.t.Error("canonical fixture differs after process-tree termination")
		}
		if p.selected != "" {
			afterEnvironment := m9Hashes(p.t, filepath.Dir(filepath.Dir(p.selected)))
			m9JSON(p.t, filepath.Join(p.dir, "after-environment-hashes.json"), afterEnvironment)
			if p.beforeEnvironment != nil && !maps.Equal(p.beforeEnvironment, afterEnvironment) {
				p.t.Error("selected environment changed during discovery/provider execution")
			}
		}
		if len(p.historicalCut) != 0 {
			cut, err := os.ReadFile(filepath.Join(p.dir, "historical-trace-cut.jsonl"))
			if err != nil || !bytes.Equal(cut, p.historicalCut) {
				p.t.Error("missing/truncated historical cut artifact")
			}
			if _, err := os.Stat(filepath.Join(p.dir, "historical-observation-plan.json")); err != nil {
				p.t.Error("missing predeclared historical observation plan")
			}
			full, err := os.ReadFile(filepath.Join(p.dir, "lsp.jsonl"))
			if err != nil || !bytes.HasPrefix(full, p.historicalCut) {
				p.t.Error("final raw trace lost/truncated the declared historical cut")
			} else {
				sum := sha256.Sum256(full)
				p.postFixCoverage["complete_raw_trace_sha256"] = hex.EncodeToString(sum[:])
				p.postFixCoverage["complete_raw_trace_bytes"] = len(full)
			}
		}
		if p.spec.known && p.t.Failed() {
			p.postFixCoverage["outcome"] = "failed"
		}
		if len(p.historicalCut) != 0 {
			m9JSON(p.t, filepath.Join(p.dir, "historical-observation.json"), p.postFixCoverage)
		}
		m9JSON(p.t, filepath.Join(p.dir, "coverage.json"), map[string]any{"case": p.spec.name, "known_expected": p.spec.known, "unknown_expected": p.spec.unknown, "selected_expected": p.selected, "natural_late_publication_observed": p.lateSeen, "later_call_updates_observed": p.updatesSeen, "complete_empty_push_quiet_checks": p.quietChecks, "progress_pending_batches_observed": p.progressVetoes, "unproven_pull_batches_observed": p.pullVetoes, "pull_veto_dimension_checks": p.pullVetoChecks, "passed": !p.t.Failed(), "post_fix": p.postFixCoverage,
			"limits": []string{"No forced producer delay or synthetic progress: timing-path coverage is reported, not assumed.", "Pyright may execute the chosen interpreter. Explicit project config is selection ownership, not dependency verification."}})
	})
	p.fixture()
	original, err := os.ReadFile(filepath.Join(p.root, "main.py"))
	if err != nil {
		p.t.Fatal(err)
	}

	p.intentionalSource = map[string]bool{string(original): true}
	// Registered before start: LIFO cleanup stops adapters/service descendants
	// before restoration, and restores even after Fatal or a context deadline.
	p.t.Cleanup(func() {
		path := filepath.Join(p.root, "main.py")
		body, err := os.ReadFile(path)
		m9JSON(p.t, filepath.Join(p.dir, "before-restore-hashes.json"), m9Hashes(p.t, p.root))
		if err != nil || !p.intentionalSource[string(body)] {
			p.t.Error("unexpected fixture source after process-tree termination; leaving it for diagnosis")
			return
		}
		m9Write(p.t, path, string(original))
	})
	p.start()
	p.call("workspace_open", nil)
	if !p.spec.cold {
		p.call("language_server_status", map[string]any{"languages": []string{"python"}})
	}
	read := p.call("read", map[string]any{"target": map[string]any{"path": "main.py"}})
	if !strings.HasPrefix(m9Text(m9Object(read["data"])["revision_id"]), "docrev_") {
		p.t.Fatal("read omitted guarded revision_id")
	}
	initial := p.edit("value: int = 1", "value: int = \"bad\"")
	if m9TypeCount(append(probeFindings(initial, "main.py", "new"), probeUpdates(initial, "main.py", "new")...)) == 0 && initial["outcome"] != "provisional" {
		p.t.Fatal("initial real type error was neither delivered nor honestly provisional")
	}
	first := p.collect()
	items := probeFindings(first, "main.py", "current")
	if p.spec.unknown && m9CodeCount(items, "reportMissingImports") != 0 {
		p.t.Fatal("ungrouped missing imports with unknown environment")
	}
	noteID := ""
	for _, item := range items {
		if m9Finding(item)["code"] == "python_environment_unresolved" {
			noteID = m9Text(item["id"])
		}
	}
	p.inspectEvidence(first)
	cursor := m9Text(m9Report(first)["cursor"])
	if cursor == "" {
		p.t.Fatal("diagnostics omitted acknowledgement cursor")
	}
	ack := p.call("diagnostics", map[string]any{"full": true, "since": cursor})
	p.ackedNote = noteID
	if p.spec.unknown {
		ackItems := probeFindings(ack, "main.py", "current")
		if m9CodeCount(ackItems, "python_environment_unresolved") != 1 || m9TypeCount(ackItems) == 0 {
			p.t.Fatal("ack lost current bodies")
		}
	}
	repeated := p.edit("marker one", "marker two")
	for _, item := range append(probeFindings(repeated, "main.py", "new"), probeUpdates(repeated, "main.py", "new")...) {
		if noteID != "" && item["id"] == noteID {
			p.t.Fatal("stable environment note reannounced as new")
		}
	}
	again := p.collect()
	if p.spec.unknown {
		for _, item := range probeFindings(again, "main.py", "current") {
			if m9Finding(item)["code"] == "python_environment_unresolved" && item["id"] != noteID {
				p.t.Fatal("note identity changed after unrelated edit")
			}
		}
		p.plain()
	}
	p.inspectEvidence(again)
	if p.spec.sandbox {
		p.sandbox()
	}
	p.interpreterSelection()
	if p.spec.known {
		path, err := filepath.EvalSymlinks(filepath.Join(p.root, "main.py"))
		if err != nil {
			p.t.Fatal(err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			p.t.Fatal(err)
		}
		fixed := strings.Replace(string(body), "value: int = \"bad\"", "value: int = 1", 1)
		digest := sha256.Sum256([]byte(fixed))
		p.postFix = &m9PostFix{Path: path, Hash: hex.EncodeToString(digest[:]), StartedUS: float64(time.Now().UnixMicro())}
		for _, item := range probeFindings(again, "main.py", "current", "stale") {
			if m9TypeCount([]map[string]any{item}) > 0 {
				id := m9Text(item["id"])
				if id == "" {
					p.t.Fatal("pre-fix type finding has no stable identity")
				}
				p.postFix.IDs = append(p.postFix.IDs, id)
			}
		}
		if len(p.postFix.IDs) == 0 {
			p.t.Fatal("post-fix gate cannot bypass the natural type-finding gate")
		}
		p.historicalFirstCall = p.sequence + 1
		p.historicalEnd = time.Now().Add(5 * time.Second)
		p.postFixCoverage["observation_plan"] = map[string]any{"sampling_ms": 5000, "declared_before_fix": true,
			"sampling_end": p.historicalEnd.UTC().Format(time.RFC3339Nano), "boundary": "final full report and evidence recovery, followed by complete newline-terminated trace byte cut",
			"limit": "Sampling is a measurement allocation; time, quiet and deadlines never prove analysis or resolution."}
		m9JSON(p.t, filepath.Join(p.dir, "historical-observation-plan.json"), p.postFixCoverage["observation_plan"])
		p.postFixCoverage["reached_gate"] = "fix_dispatch"
		p.postFixCoverage["original_finding_ids"] = p.postFix.IDs
		fix := p.edit("value: int = \"bad\"", "value: int = 1")
		data := m9Object(fix["data"])
		if m9Text(fix["request_id"]) == "" {
			p.t.Fatal("fix receipt omitted transaction request identity")
		}
		p.postFix.Transaction = "edit_" + m9Text(fix["request_id"])
		p.postFix.Revision = m9Text(data["document_revision"])
		if p.postFix.Revision == "" {
			p.postFix.Revision = m9Text(m9Object(data["document_revisions"])["main.py"])
		}
		if !strings.HasPrefix(p.postFix.Revision, "docrev_") {
			p.t.Fatal("fixed postimage omitted exact receipt document revision")
		}
		p.postFixCoverage["fixed_document_revision"] = p.postFix.Revision
		p.postFixCoverage["fixed_canonical_path"] = p.postFix.Path
		p.postFixCoverage["fixed_sha256"] = p.postFix.Hash
		p.collectPostFix()
	}
}

func (p *m9Probe) plain() {
	wire := p.connect(filepath.Join(p.dir, "s"), "plain")
	call := func(name string, args map[string]any) string {
		args["root"] = p.root
		result := wire.rpc(p.t, "tools/call", map[string]any{"name": name, "arguments": args})
		if result["structuredContent"] != nil {
			p.t.Fatal("plain unexpectedly has structuredContent")
		}
		var text strings.Builder
		for _, raw := range probeSlice(result["content"]) {
			text.WriteString(m9Text(m9Object(raw)["text"]))
		}
		return text.String()
	}
	jsonRead := p.call("read", map[string]any{"target": map[string]any{"path": "main.py"}})
	revision := m9Text(m9Object(jsonRead["data"])["revision_id"])
	text := call("read", map[string]any{"target": map[string]any{"path": "main.py"}})
	if !strings.Contains(text, revision) {
		p.t.Fatal("plain read lost exact JSON document guard")
	}
	body, err := os.ReadFile(filepath.Join(p.root, "main.py"))
	if err != nil {
		p.t.Fatal(err)
	}
	if !p.intentionalSource[string(body)] || strings.Count(string(body), "marker two") != 1 {
		p.t.Fatal("plain edit does not match the expected fixture source")
	}
	body = []byte(strings.Replace(string(body), "marker two", "marker three", 1))
	p.intentionalSource[string(body)] = true
	text = call("edit_apply", map[string]any{"format": false, "operation": map[string]any{"kind": "create_file", "path": "main.py", "replace": true, "revision_id": revision, "content": string(body)}})
	m9Write(p.t, filepath.Join(p.dir, "plain-edit.txt"), text)
	if !strings.Contains(text, "revision_id=docrev_") || !strings.Contains(text, "wsrev_") {
		p.t.Fatal("plain edit lost follow-up guards")
	}
	p.collect()
	text = call("diagnostics", map[string]any{"full": true, "since": fmt.Sprintf("diagcur_%d", p.acknowledgedCursor)})
	m9Write(p.t, filepath.Join(p.dir, "plain-diagnostics.txt"), text)
	for _, want := range []string{"python_environment_unresolved", "provisional", "reportAssignmentType"} {
		if !strings.Contains(text, want) {
			p.t.Fatalf("plain diagnostics dropped %s", want)
		}
	}
}

func (p *m9Probe) sandbox() {
	reply := p.call("change_plan", map[string]any{"action": "prepare", "accept_provisional": true, "operations": []any{map[string]any{
		"op_id": "python-sandbox", "kind": "create_file", "path": "sandbox_check.py", "content": "import m9_fixture_dep\nvalue: int = \"bad\"\n",
	}}})
	plan := m9Object(m9Object(reply["data"])["plan"])
	preparation := m9Object(plan["preparation"])
	revision := m9Text(preparation["prepared_revision"])
	if revision == "" {
		p.t.Fatalf("sandbox preparation missing guarded identity: %+v", reply)
	}
	staged := p.call("diagnostics", map[string]any{"revision": revision, "full": true})
	encoded, _ := json.Marshal(staged)
	if !strings.Contains(string(encoded), "reportAssignmentType") && m9Report(staged)["confidence"] != "provisional" && staged["outcome"] != "provisional" {
		p.t.Fatal("sandbox lost real type error without disclosing provisional coverage")
	}
	if !p.spec.relativeConfig && (strings.Contains(string(encoded), "reportMissingImports") || strings.Contains(string(encoded), "python_environment_unresolved")) {
		p.t.Fatal("automatic sandbox environment lost canonical dependency")
	}
	p.call("change_plan", map[string]any{"action": "discard", "plan_id": plan["plan_id"], "plan_revision": plan["plan_revision"]})
	// Actual selected settings and publication paths (including the
	// sandbox-relative explicit-config outcome) are retained in lsp.jsonl.
}

func (p *m9Probe) interpreterSelection() {
	body, err := os.ReadFile(filepath.Join(p.dir, "lsp.jsonl"))
	if err != nil {
		p.t.Fatal(err)
	}
	attached := false
	sandboxAttached := false
	rawMissingPeak := 0
	for _, line := range strings.Split(string(body), "\n") {
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		if event["kind"] == "publish" || event["kind"] == "pull" {
			params := m9Object(event["params"])
			uri := m9Text(params["uri"])
			diagnostics := probeSlice(params["diagnostics"])
			if event["kind"] == "pull" {
				uri = m9Text(m9Object(m9Object(event["request_params"])["textDocument"])["uri"])
				diagnostics = probeSlice(params["items"])
			}
			if strings.HasSuffix(uri, "/main.py") {
				count := 0
				for _, raw := range diagnostics {
					if m9Object(raw)["code"] == "reportMissingImports" {
						count++
					}
				}
				if count > rawMissingPeak {
					rawMissingPeak = count
				}
			}
		}
		if event["kind"] != "attach" || event["name"] != "pyright" {
			continue
		}
		attached = true
		path := m9Text(event["path"])
		actual := m9Text(m9Object(m9Object(event["settings"])["python"])["pythonPath"])
		if p.spec.known && !p.spec.relativeConfig && actual != p.selected {
			p.t.Fatalf("attached interpreter %q, want %q", actual, p.selected)
		}
		if p.spec.unknown && actual != "" {
			p.t.Fatalf("unknown environment acquired pythonPath %q", actual)
		}
		if p.spec.sandbox && !strings.HasPrefix(path, p.root+string(filepath.Separator)) {
			sandboxAttached = true
		}
	}
	if p.spec.unknown && rawMissingPeak < 2 {
		p.t.Fatal("real Pyright did not report multiple raw missing imports through push or pull")
	}
	m9JSON(p.t, filepath.Join(p.dir, "raw-publication-summary.json"), map[string]any{"raw_missing_import_peak": rawMissingPeak, "sandbox_attached": sandboxAttached})
	if !attached || (p.spec.sandbox && !sandboxAttached) {
		p.t.Fatal("real expected Pyright attachment was not traced")
	}
	status := p.call("language_server_status", map[string]any{"languages": []string{"python"}})
	statusBody, _ := json.Marshal(status)
	if p.spec.reason != "" && !strings.Contains(string(statusBody), p.spec.reason) {
		p.t.Fatalf("status omitted discovery reason %s: %s", p.spec.reason, statusBody)
	}
}

func (p *m9Probe) assertManagerTrace() {
	files, err := filepath.Glob(filepath.Join(p.dir, "exec.*"))
	if err != nil || len(files) == 0 {
		p.t.Fatal("missing actual execve trace")
	}
	traces := ""
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			p.t.Fatal(err)
		}
		traces += string(data)
	}
	for _, manager := range []string{"uv", "poetry"} {
		real, err := filepath.EvalSymlinks(filepath.Join(p.tools, "bin", manager))
		if err != nil {
			p.t.Fatal(err)
		}
		count := strings.Count(traces, "execve("+strconv.Quote(real)+",")
		expect := p.spec.trusted && p.spec.manager == manager && !p.spec.local && !p.spec.missingManager
		if expect && count == 0 {
			p.t.Fatalf("real %s fallback was not executed", manager)
		}
		if !expect && m9ManagerExec(traces, manager) {
			p.t.Fatalf("unexpected %s execution (any path or Python module spelling); see exec.*", manager)
		}
	}
}

func (p *m9Probe) inspectEvidence(reply map[string]any) {
	found := false
	var pullVetoIDs []string
	for _, raw := range probeSlice(m9Object(reply["evidence"])["ids"]) {
		id := m9Text(raw)
		if id == "" {
			continue
		}
		found = true
		batch := p.evidenceBatches[id]
		fresh := !p.evidenceSeen[id]
		if fresh {
			p.evidenceSeen[id] = true
			evidence := p.call("evidence_get", map[string]any{"evidence_id": id})
			record := m9Object(m9Object(evidence["data"])["evidence"])
			batch = m9Object(record["payload"])
			if text, ok := record["payload"].(string); ok {
				_ = json.Unmarshal([]byte(text), &batch)
			}
			p.evidenceBatches[id] = batch
		}
		kind := m9Text(batch["kind"])
		if kind != "lsp_push" && kind != "lsp_pull" {
			continue
		}
		if fresh && batch["progress_pending"] == true {
			p.progressVetoes++
		}
		trace, _ := os.ReadFile(filepath.Join(p.dir, "lsp.jsonl"))
		for _, line := range strings.Split(string(trace), "\n") {
			var event map[string]any
			if json.Unmarshal([]byte(line), &event) != nil || event["kind"] != "publish" {
				continue
			}
			params := m9Object(event["params"])
			at, _ := event["unix_us"].(float64)
			if params["version"] == batch["document_version"] && batch["document_version"] != nil &&
				strings.HasSuffix(m9Text(params["uri"]), "/main.py") && len(probeSlice(params["diagnostics"])) > 0 &&
				at > float64(p.lastEditAt.UnixMicro()) {
				p.lateSeen = true
			}
		}
		if kind == "lsp_pull" && !m9EffectiveProof(batch) {
			pullVetoIDs = append(pullVetoIDs, id)
			if fresh {
				p.pullVetoes++
			}
		}
		if batch["complete"] == true {
			version, vok := batch["document_version"].(float64)
			expected, eok := batch["expected_version"].(float64)
			if kind == "lsp_push" && (!vok || !eok || version < expected || batch["progress_pending"] == true) {
				p.t.Fatalf("false complete push version/progress evidence: %+v", batch)
			}
			if batch["reason"] == "python_environment_unresolved" {
				p.t.Fatal("grouped evidence claimed complete")
			}
			// Quiet is asserted only for actual complete empty pushes. Pulls
			// and positive pushes do not need an empty-publication quiet wait.
			if kind == "lsp_push" && len(probeSlice(batch["findings"])) == 0 {
				p.assertQuiet(batch)
				if fresh {
					p.quietChecks++
				}
			}
		}
	}
	// Evaluate after all cited batches have been recovered, so a current valid
	// batch may supersede an older vetoed pull in the same coverage dimension.
	for _, id := range pullVetoIDs {
		p.assertPullVeto(reply, id)
	}
	if !found {
		p.t.Fatal("real diagnostic report omitted recoverable evidence IDs")
	}
}

func (p *m9Probe) assertQuiet(batch map[string]any) {
	observed, err := time.Parse(time.RFC3339Nano, m9Text(batch["observed_at"]))
	if err != nil {
		p.t.Fatal("missing observation timestamp")
	}
	body, err := os.ReadFile(filepath.Join(p.dir, "lsp.jsonl"))
	if err != nil {
		p.t.Fatal(err)
	}
	var last float64
	for _, line := range strings.Split(string(body), "\n") {
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) != nil || event["kind"] != "publish" {
			continue
		}
		params := m9Object(event["params"])
		if params["version"] != batch["document_version"] || len(probeSlice(params["diagnostics"])) != 0 {
			continue
		}
		if !strings.HasSuffix(m9Text(params["uri"]), "/main.py") {
			continue
		}
		at, _ := event["unix_us"].(float64)
		if at <= float64(observed.UnixMicro()) && at > last {
			last = at
		}
	}
	if last == 0 || float64(observed.UnixMicro())-last < 150000 {
		p.t.Fatalf("complete empty push lacks observed 150ms quiet: %+v", batch)
	}
}

// Hash only owned fixture/tool trees. Symlinks are recorded, never followed.
// Stream regular files with explicit entry/byte bounds, including mode changes.
func m9Hashes(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == ".git" && entry.IsDir() {
			return filepath.SkipDir
		}
		if len(result) >= 100000 {
			return fmt.Errorf("hash entry bound exceeded: %s", root)
		}
		rel, _ := filepath.Rel(root, path)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		prefix := info.Mode().String() + ":"
		if entry.IsDir() {
			result[rel] = prefix
			return nil
		}
		h := sha256.New()
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_, _ = io.WriteString(h, target)
		} else {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("non-regular immutable file: %s", path)
			}
			total += info.Size()
			if info.Size() > 512<<20 || total > 4<<30 {
				return fmt.Errorf("hash byte bound exceeded: %s", root)
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			n, copyErr := io.Copy(h, io.LimitReader(f, info.Size()+1))
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if n != info.Size() {
				return fmt.Errorf("file changed while hashing: %s", path)
			}
		}
		result[rel] = prefix + hex.EncodeToString(h.Sum(nil))
		return nil
	})
	if err != nil {
		t.Fatalf("immutable snapshot failed: %v", err)
	}
	return result
}

// Include the actual executable behind each tool symlink, including the
// read-only host shell/Git/strace prerequisites; never traverse host trees.
func m9ToolHashes(t *testing.T, tools, binary string) map[string]string {
	result := m9Hashes(t, tools)
	for _, name := range []string{"python", "python3", "uv", "poetry", "nvim", "node", "pyright-langserver", "gopls", "strace", "git", "sh", "bash", "readlink", "uname", "dirname", "realpath"} {
		path, err := filepath.EvalSymlinks(filepath.Join(tools, "bin", name))
		if err != nil {
			t.Fatal(err)
		}
		result["resolved-executable:"+name+":"+path] = m9Hashes(t, path)["."]
	}
	result["private-huyang:"+binary] = m9Hashes(t, binary)["."]
	return result
}

func m9Within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Both source entrypoints enforce this, even without the documented wrapper.
func m9ScratchHome(t *testing.T) {
	t.Helper()
	home := os.Getenv("HOME")
	expected := os.Getenv("HUYANG_M9_EXPECTED_HOME")
	if expected == "" {
		expected = "/home/job"
	}
	real, err := filepath.EvalSymlinks(home)
	if err != nil || !filepath.IsAbs(home) || home != expected || real != home ||
		real == "/" || real == "/root" || strings.HasPrefix(real+"/", "/root/") ||
		(strings.HasPrefix(real+"/", "/home/") && real != "/home/job" && !strings.HasPrefix(real, "/home/job/")) ||
		strings.HasPrefix(real+"/", "/tmp/") {
		t.Fatal("HOME must be canonical job scratch (/home/job or explicit HUYANG_M9_EXPECTED_HOME), never a real home")
	}
	mapping, err := os.ReadFile("/proc/self/uid_map")
	fields := strings.Fields(string(mapping))
	if err != nil || len(fields) < 3 || (len(fields) == 3 && fields[0] == "0" && fields[1] == "0" && fields[2] == "4294967295") {
		t.Fatal("a non-initial UID namespace is required before any probe writes")
	}
	info, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 {
		t.Fatal("scratch HOME must be owned by this UID and not writable by other users")
	}
}

func m9PrivateDir(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("private directory must be owned and mode 0700: %s", path)
	}
}

// Metadata only: no HOME file contents, credentials or unrelated host trees.
// The run parent is the sole allowed mutable subtree; tools there are hashed
// separately. A fallback writer elsewhere in scratch HOME fails the run.
func m9HomeMetadata(t *testing.T, allowed string) map[string]string {
	t.Helper()
	home := os.Getenv("HOME")
	result := map[string]string{}
	err := filepath.WalkDir(home, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == allowed {
			return filepath.SkipDir
		}
		if len(result) >= 20000 {
			return fmt.Errorf("scratch HOME inventory exceeds 20000 entries")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(home, path)
		// Parent directory mtimes can change when the allowed root is created;
		// it already exists at both snapshots. Do not read symlink targets.
		result[rel] = fmt.Sprintf("%s:%d:%d", info.Mode(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatalf("scratch HOME inventory failed: %v", err)
	}
	return result
}

func m9ManagerExec(trace, manager string) bool {
	// Decode every quoted strace string (paths AND argv), accepting any path
	// spelling and execveat. Checking all strings is deliberately conservative.
	quoted := regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
	for _, line := range strings.Split(trace, "\n") {
		if !strings.Contains(line, "execve(") && !strings.Contains(line, "execveat(") {
			continue
		}
		var previous string
		for _, encoded := range quoted.FindAllString(line, -1) {
			value, err := strconv.Unquote(encoded)
			if err != nil {
				continue
			}
			if filepath.Base(value) == manager || strings.HasSuffix(value, "/"+manager+"/__main__.py") ||
				value == "-m"+manager || strings.HasPrefix(value, "-m"+manager+".") ||
				(previous == "-m" && (value == manager || strings.HasPrefix(value, manager+"."))) {
				return true
			}
			previous = value
		}
	}
	return false
}

// Pure regressions for the safety checks; no subprocesses or filesystem writes.
func TestM9ManagerTraceSpellings(t *testing.T) {
	for _, trace := range []string{
		`execve("/case/bin/poetry", ["poetry"], 0x0) = 0`,
		`execve("/tools/bin/poetry", ["poetry"], 0x0) = 0`,
		`execveat(3, "poetry", ["poetry"], 0x0, 0) = 0`,
		`execve("/bin/python", ["python", "-m", "poetry"], 0x0) = 0`,
		`execve("/bin/python", ["python", "-mpoetry"], 0x0) = 0`,
		`execve("/bin/python", ["python", "/tool/poetry/__main__.py"], 0x0) = 0`,
	} {
		if !m9ManagerExec(trace, "poetry") {
			t.Errorf("missed manager execution: %s", trace)
		}
	}
	if !m9ManagerExec(`execveat(AT_FDCWD, "/case/bin/uv", ["uv"], 0x0, 0) = 0`, "uv") {
		t.Error("missed uv execveat")
	}
	if m9ManagerExec(`execve("/bin/python", ["python", "-c", "print('poetry')"], 0x0) = 0`, "poetry") {
		t.Error("ordinary Python statement treated as manager execution")
	}
}

func TestM9EffectivePullProof(t *testing.T) {
	valid := map[string]any{"complete": true, "selected": true, "document_version": float64(4), "expected_version": float64(4)}
	if !m9EffectiveProof(valid) {
		t.Fatal("current complete pull rejected")
	}
	for _, change := range []map[string]any{
		{"document_version": nil}, {"expected_version": nil}, {"document_version": float64(3)},
		{"progress_pending": true}, {"timed_out": true}, {"complete": false},
		{"selected": false}, {"reason": "python_environment_unresolved"},
	} {
		batch := maps.Clone(valid)
		maps.Copy(batch, change)
		if m9EffectiveProof(batch) {
			t.Errorf("unproven raw pull accepted: %+v", batch)
		}
	}
	// Coverage retains R1's veto after a proven R2 arrives from the same provider.
	veto := map[string]any{"document": "main.py", "provider_id": "pyright", "document_revision": "R1", "observed_at": "2026-10-17T00:00:00Z"}
	candidate := maps.Clone(valid)
	maps.Copy(candidate, map[string]any{"document": "main.py", "provider_id": "pyright", "document_revision": "R2", "observed_at": "2026-10-17T00:00:01Z"})
	if !m9SupersedesPullVeto(candidate, veto) {
		t.Fatal("later proven R2 did not supersede vetoed R1")
	}
	candidate["observed_at"] = veto["observed_at"]
	if !m9SupersedesPullVeto(candidate, veto) {
		t.Fatal("equal observation timestamp should permit supersession")
	}
	for _, change := range []map[string]any{
		{"document": "other.py"}, {"provider_id": "other-provider"},
		{"observed_at": "2026-10-16T23:59:59Z"}, {"progress_pending": true},
	} {
		other := maps.Clone(candidate)
		maps.Copy(other, change)
		if m9SupersedesPullVeto(other, veto) {
			t.Errorf("unrelated, older or unproven batch superseded veto: %+v", other)
		}
	}
}

// A protocol-shaped trace fixture: dispatch, transport acceptance and callback
// are separate observations. No service/client runs in these policy regressions.
func m9PolicyFixture(t *testing.T) (*m9PostFix, []map[string]any) {
	t.Helper()
	const trace = `
{"kind":"attach","trace_seq":1,"process_id":91,"client_id":7,"name":"pyright","path":"/project/main.py"}
{"kind":"pull_request","trace_seq":2,"process_id":91,"client_id":7,"request_id":"91:7:1","unix_us":20,"request_params":{"textDocument":{"uri":"file:///project/main.py"}},"snapshot":{"path":"/project/main.py","uri":"file:///project/main.py","bufnr":1,"changedtick":8,"local_version":4,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","disk_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","attached":true,"client_current":true}}
{"kind":"pull_dispatch","trace_seq":3,"process_id":91,"client_id":7,"request_id":"91:7:1","sent":true,"rpc_id":13}
{"kind":"pull","trace_seq":4,"process_id":91,"client_id":7,"callback_client_id":7,"request_id":"91:7:1","unix_us":30,"request_params":{"textDocument":{"uri":"file:///project/main.py"}},"params":{"kind":"full","items":[],"resultId":"opaque-5"},"snapshot":{"path":"/project/main.py","uri":"file:///project/main.py","bufnr":1,"changedtick":8,"local_version":4,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","disk_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","attached":true,"client_current":true}}
`
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(trace), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return &m9PostFix{Path: "/project/main.py", Hash: strings.Repeat("a", 64),
		Revision: "docrev_fixed", Transaction: "edit_fix", StartedUS: 10, IDs: []string{"type-original"}}, events
}

func TestM9PostFixCorrelation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(request, response map[string]any)
	}{
		{"pre-fix-inflight-empty", func(a, b map[string]any) { a["unix_us"] = float64(5) }},
		{"wrong-callback-client", func(a, b map[string]any) { b["callback_client_id"] = float64(8) }},
		{"other-provider-process", func(a, b map[string]any) { b["process_id"] = float64(92) }},
		{"uncorrelated-response", func(a, b map[string]any) { b["request_id"] = "91:7:old" }},
		{"error", func(a, b map[string]any) { b["err"] = map[string]any{"code": -32603} }},
		{"cancelled", func(a, b map[string]any) { b["err"] = map[string]any{"code": -32800} }},
		{"timed-out", func(a, b map[string]any) { b["err"] = map[string]any{"message": "timeout"} }},
		{"nil-result", func(a, b map[string]any) { b["params"] = nil }},
		{"unchanged", func(a, b map[string]any) { b["params"] = map[string]any{"kind": "unchanged", "resultId": "opaque-6"} }},
		{"missing-items", func(a, b map[string]any) { delete(m9Object(b["params"]), "items") }},
		{"object-is-not-array", func(a, b map[string]any) { m9Object(b["params"])["items"] = map[string]any{} }},
		{"null-is-not-array", func(a, b map[string]any) { m9Object(b["params"])["items"] = nil }},
		{"nonempty", func(a, b map[string]any) {
			m9Object(b["params"])["items"] = []any{map[string]any{"message": "still analyzing"}}
		}},
		{"sandbox-request", func(a, b map[string]any) {
			m9Object(m9Object(a["request_params"])["textDocument"])["uri"] = "file:///sandbox/main.py"
		}},
		{"pre-fix-bytes", func(a, b map[string]any) { m9Object(a["snapshot"])["sha256"] = "old" }},
		{"unsynchronized-disk", func(a, b map[string]any) { m9Object(a["snapshot"])["disk_sha256"] = "old" }},
		{"changed-during-request", func(a, b map[string]any) { m9Object(b["snapshot"])["changedtick"] = float64(9) }},
		{"advanced-version", func(a, b map[string]any) { m9Object(b["snapshot"])["local_version"] = float64(5) }},
		{"missing-version", func(a, b map[string]any) { delete(m9Object(a["snapshot"]), "local_version") }},
		{"detached-client", func(a, b map[string]any) { m9Object(b["snapshot"])["attached"] = false }},
		{"replaced-client", func(a, b map[string]any) { m9Object(b["snapshot"])["client_current"] = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixed, events := m9PolicyFixture(t)
			test.mutate(events[1], events[3])
			if err := m9CorrelatedEmpty(events[1], events[3], fixed); err == nil {
				t.Fatal("unsafe post-fix observation accepted")
			}
		})
	}
	fixed, events := m9PolicyFixture(t)
	if err := m9CorrelatedEmpty(events[1], events[3], fixed); err != nil {
		t.Fatal(err)
	}
	// Local request version and opaque resultId are not producer proof.
	state, err := m9PostFixTrace(events, fixed)
	if err != nil || state.Candidate == nil || state.ProducerProof {
		t.Fatalf("honest pure-pull observation misclassified: %+v, %v", state, err)
	}
}

func TestM9PostFixTransientProgress(t *testing.T) {
	progress := func(seq float64, token string, kind string) map[string]any {
		return map[string]any{"kind": "progress", "trace_seq": seq, "process_id": float64(91),
			"data": map[string]any{"client_id": float64(7), "params": map[string]any{"token": token, "value": map[string]any{"kind": kind}}}}
	}
	fixed, events := m9PolicyFixture(t)
	check := func(wantCandidate, wantPending bool) m9TraceState {
		t.Helper()
		state, err := m9PostFixTrace(events, fixed)
		if err != nil || (state.Candidate != nil) != wantCandidate || state.Pending != wantPending {
			t.Fatalf("candidate=%v pending=%v; got %+v, %v", wantCandidate, wantPending, state, err)
		}
		return state
	}
	first := check(true, false)
	events = append(events, progress(5, "analysis", "begin"))
	check(false, true) // Empty before a real progress begin must be discarded.
	events = append(events, progress(6, "index", "begin"), progress(7, "analysis", "end"))
	check(false, true) // One end cannot clear another token's pending work.
	events = append(events, progress(8, "index", "end"))
	check(false, false) // End alone cannot restore the superseded empty.

	// A fresh native request after known work ends can qualify, without
	// inventing another progress cycle or claiming the producer analyzed it.
	_, fresh := m9PolicyFixture(t)
	for i, event := range fresh[1:] {
		event["trace_seq"] = float64(9 + i)
		event["request_id"] = "91:7:2"
		events = append(events, event)
	}
	last := check(true, false)
	if last.Epoch == first.Epoch || last.Candidate.Request["request_id"] != "91:7:2" {
		t.Fatal("old candidate survived known work")
	}
	contradiction := maps.Clone(fresh[3])
	contradiction["trace_seq"] = float64(12)
	contradiction["params"] = map[string]any{"kind": "full", "items": []any{map[string]any{"code": "reportAssignmentType"}}}
	events = append(events, contradiction)
	check(false, false)

	// Progress on another client is tracked independently, not as selected work.
	_, events = m9PolicyFixture(t)
	other := progress(5, "other", "begin")
	m9Object(other["data"])["client_id"] = float64(8)
	events = append(events, other)
	check(true, false)

	// Dispatch during work, completion after its end: an empty body still
	// cannot substitute for a request made after that known work ended.
	_, events = m9PolicyFixture(t)
	events = append(events, progress(5, "analysis", "begin"))
	_, during := m9PolicyFixture(t)
	during[1]["trace_seq"], during[2]["trace_seq"], during[3]["trace_seq"] = float64(6), float64(7), float64(9)
	for _, event := range during[1:] {
		event["request_id"] = "91:7:during"
	}
	events = append(events, during[1], during[2], progress(8, "analysis", "end"), during[3])
	check(false, false)
}

func TestM9PostFixTransportAndEvidence(t *testing.T) {
	fixed, events := m9PolicyFixture(t)
	state, err := m9PostFixTrace(events, fixed)
	if err != nil {
		t.Fatal(err)
	}
	batch := map[string]any{"kind": "lsp_pull", "selected": true, "complete": false,
		"provider_id": "pyright#7", "document": "/project/main.py", "document_revision": "docrev_fixed",
		"transaction_id": "edit_fix", "expected_version": float64(4), "dimension": "edited_documents",
		"result_id": "opaque-5", "observed_at": "1970-01-01T00:00:00.000031Z"}
	if !m9PostFixBatch(batch, fixed, state, false) || m9PostFixBatch(batch, fixed, state, true) {
		t.Fatal("receipt-correlated incomplete pull must remain unproven")
	}
	for _, change := range []map[string]any{
		{"document_revision": "docrev_before"}, {"transaction_id": "edit_old"}, {"provider_id": "pyright#8"},
		{"document": "/sandbox/main.py"}, {"selected": false}, {"staged": true}, {"timed_out": true},
		{"progress_pending": true}, {"complete": true}, {"document_version": float64(4)},
		{"expected_version": float64(3)}, {"observed_at": "1970-01-01T00:00:00.000029Z"},
		{"result_id": "different-opaque-result"},
	} {
		bad := maps.Clone(batch)
		maps.Copy(bad, change)
		if m9PostFixBatch(bad, fixed, state, false) {
			t.Errorf("uncorrelated/invented evidence accepted: %+v", change)
		}
	}
	// An anonymous full response is locally observable, but cannot correlate
	// selected evidence against an adjacent timeout/non-full empty conversion.
	delete(m9Object(state.Candidate.Response["params"]), "resultId")
	anonymous := maps.Clone(batch)
	delete(anonymous, "result_id")
	if m9PostFixBatch(anonymous, fixed, state, false) {
		t.Fatal("anonymous response supplied false evidence correlation")
	}
	m9Object(state.Candidate.Response["params"])["resultId"] = "opaque-5"
	// A current stamped producer path cannot be laundered through fallback.
	events = append(events, map[string]any{"kind": "publish", "trace_seq": float64(5), "unix_us": float64(30),
		"process_id": float64(91), "client_id": float64(7),
		"params": map[string]any{"uri": fixed.uri(), "version": float64(4), "diagnostics": []any{}}})
	state, err = m9PostFixTrace(events, fixed)
	if err != nil || !state.ProducerProof {
		t.Fatalf("stamped producer proof not observed: %+v, %v", state, err)
	}
	push := maps.Clone(batch)
	maps.Copy(push, map[string]any{"kind": "lsp_push", "complete": true, "document_version": float64(4)})
	if !m9PostFixBatch(push, fixed, state, true) {
		t.Fatal("real selected stamped push was not eligible for the existing authority/quiet gates")
	}
	state.ProofBarrier = 6 // Newer observed work cannot reuse an older clean push.
	if m9PostFixBatch(push, fixed, state, true) {
		t.Fatal("old stamped push survived newer known work")
	}
	// Unversioned publications are explicitly mixed/unsupported, not pure pull.
	delete(m9Object(events[4]["params"]), "version")
	state, err = m9PostFixTrace(events, fixed)
	if err != nil || state.ProducerProof || !state.UnversionedPush {
		t.Fatalf("unversioned push was hidden: %+v, %v", state, err)
	}
	_, events = m9PolicyFixture(t)
	events = append(events, map[string]any{"kind": "pull_cancel", "trace_seq": float64(5), "process_id": float64(91), "client_id": float64(7), "request_id": "91:7:1"})
	state, err = m9PostFixTrace(events, fixed)
	if err != nil || state.Candidate != nil {
		t.Fatal("cancelled request accepted")
	}
	for _, transport := range []map[string]any{
		{"sent": false}, {"rpc_id": nil},
	} {
		_, events = m9PolicyFixture(t)
		maps.Copy(events[2], transport)
		state, err = m9PostFixTrace(events, fixed)
		if err != nil || state.Candidate != nil {
			t.Fatalf("unaccepted transport qualified: %+v, %v", state, err)
		}
	}
	_, events = m9PolicyFixture(t)
	state, err = m9PostFixTrace(events[:3], fixed)
	if err != nil || state.Candidate != nil || !state.Pending {
		t.Fatalf("outstanding request treated as completed: %+v, %v", state, err)
	}
}

func TestM9PostFixFindingRetention(t *testing.T) {
	fixed, _ := m9PolicyFixture(t)
	for _, body := range []string{
		`{"data":{"diagnostics":{"confidence":"provisional","current":[{"id":"type-original","status":"current"}]}}}`,
		`{"data":{"diagnostics":{"confidence":"provisional","stale":[{"id":"type-original","status":"stale"}]}}}`,
	} {
		var reply map[string]any
		if err := json.Unmarshal([]byte(body), &reply); err != nil {
			t.Fatal(err)
		}
		if _, err := m9PostFixLedger(reply, fixed, true); err != nil {
			t.Fatal(err)
		}
	}
	for _, body := range []string{
		`{"data":{"diagnostics":{"confidence":"authoritative","stale":[{"id":"type-original","status":"stale"}]}}}`,
		`{"data":{"diagnostics":{"confidence":"provisional","current":[],"stale":[]}}}`,
		`{"data":{"diagnostic_delta":{"resolved":["type-original"]}}}`,
		`{"data":{"diagnostics":{"notices":[{"id":"type-original","kind":"resolved"}]}}}`,
		`{"diagnostic_updates":[{"id":"type-original","kind":"resolved","path":"main.py"}]}`,
		`{"data":{"diagnostic_delta":{"resolved_ids":["type-original"]}}}`,
		`{"diagnostic_updates_truncated":true}`,
	} {
		var reply map[string]any
		if err := json.Unmarshal([]byte(body), &reply); err != nil {
			t.Fatal(err)
		}
		if _, err := m9PostFixLedger(reply, fixed, true); err == nil {
			t.Errorf("false authority, lost identity or hidden resolution accepted: %s", body)
		}
	}
}

func m9EffectiveProof(batch map[string]any) bool {
	version, vok := batch["document_version"].(float64)
	expected, eok := batch["expected_version"].(float64)
	return batch["complete"] == true && vok && eok && version >= expected &&
		batch["selected"] == true && batch["progress_pending"] != true &&
		batch["timed_out"] != true && batch["reason"] != "python_environment_unresolved"
}

func m9SupersedesPullVeto(candidate, veto map[string]any) bool {
	candidateAt, candidateErr := time.Parse(time.RFC3339Nano, m9Text(candidate["observed_at"]))
	vetoAt, vetoErr := time.Parse(time.RFC3339Nano, m9Text(veto["observed_at"]))
	return m9EffectiveProof(candidate) && candidateErr == nil && vetoErr == nil &&
		!candidateAt.Before(vetoAt) && candidate["document"] == veto["document"] &&
		candidate["provider_id"] == veto["provider_id"]
}

func (p *m9Probe) assertPullVeto(reply map[string]any, vetoID string) {
	// Evidence can include superseded batches. Judge the dimensions which
	// actually cite this batch; a separate current proven batch may supersede it.
	report := m9Report(reply)
	for _, raw := range m9Object(report["coverage"]) {
		dimension := m9Object(raw)
		citesVeto, proven := false, false
		for _, rawID := range probeSlice(dimension["evidence_ids"]) {
			id := m9Text(rawID)
			citesVeto = citesVeto || id == vetoID
			proven = proven || m9SupersedesPullVeto(p.evidenceBatches[id], p.evidenceBatches[vetoID])
		}
		if citesVeto && !proven {
			p.pullVetoChecks++
		}
		if citesVeto && !proven && (dimension["confidence"] == "authoritative" || report["confidence"] == "authoritative") {
			p.t.Fatalf("unproven pull promoted to authoritative diagnostics: %+v", reply)
		}
	}
}

type m9Process struct {
	pid, ppid, group int
	start            string
}

// /proc stat contains process identity only, never environments, cmdlines or keys.
func m9Processes() (map[int]m9Process, error) {
	result := map[int]m9Process{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		body, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		} // A concurrent exit is normal.
		end := strings.LastIndexByte(string(body), ')')
		if end < 0 {
			continue
		}
		fields := strings.Fields(string(body[end+1:]))
		if len(fields) < 20 {
			continue
		}
		ppid, _ := strconv.Atoi(fields[1])
		group, _ := strconv.Atoi(fields[2])
		result[pid] = m9Process{pid: pid, ppid: ppid, group: group, start: fields[19]}
	}
	return result, nil
}

// Retain identities across tracer exits; track separate provider process groups.
// Fixture commands own a group too. strace additionally uses --kill-on-exit so
// its tracees cannot survive an abrupt tracer exit between /proc observations.
func m9TrackTree(cmd *exec.Cmd) func(*testing.T) {
	leader := cmd.Process.Pid
	owned := map[int]m9Process{}
	var mu sync.Mutex
	var scanErr error
	scan := func() map[int]m9Process {
		all, err := m9Processes()
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			scanErr = err
		}
		if proc, ok := all[leader]; ok {
			if old, tracked := owned[leader]; !tracked || old.start == proc.start {
				owned[leader] = proc
			}
		}
		for changed := true; changed; {
			changed = false
			for pid, proc := range all {
				parent, tracked := owned[proc.ppid]
				if live, ok := all[proc.ppid]; tracked && ok && parent.start == live.start {
					if _, found := owned[pid]; !found {
						owned[pid] = proc
						changed = true
					}
				}
			}
		}
		live := map[int]m9Process{}
		for pid, old := range owned {
			if proc, ok := all[pid]; ok && proc.start == old.start {
				live[pid] = proc
			}
		}
		return live
	}
	_ = scan()
	finish, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-finish:
				return
			case <-ticker.C:
				_ = scan()
			}
		}
	}()
	return func(t *testing.T) {
		t.Helper()
		defer func() { close(finish); <-stopped }()
		deadline, killAt := time.Now().Add(6*time.Second), time.Now().Add(3*time.Second)
		for {
			live := scan()
			groupErr := unix.Kill(-leader, 0)
			mu.Lock()
			observationErr := scanErr
			mu.Unlock()
			if len(live) == 0 && groupErr == unix.ESRCH && observationErr == nil {
				return
			}
			signal := unix.SIGTERM
			if !time.Now().Before(killAt) {
				signal = unix.SIGKILL
			}
			_ = unix.Kill(-leader, signal)
			for pid := range live {
				_ = unix.Kill(pid, signal)
			}
			if !time.Now().Before(deadline) {
				t.Errorf("process tree/group persists after TERM/KILL: leader=%d live=%v group_error=%v proc_error=%v", leader, live, groupErr, observationErr)
				return
			}
			// Condition polling with a deadline, not a warmup sleep.
			_, _ = unix.Poll(nil, 20)
		}
	}
}

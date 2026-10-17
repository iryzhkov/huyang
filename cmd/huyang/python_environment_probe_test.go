//go:build linux

package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	baseEnv := []string{"HOME=" + os.Getenv("HOME"), "LANG=C.UTF-8", "HUYANG_FRICTION=0",
		"XDG_CONFIG_HOME=" + filepath.Join(out, "preflight-config"), "XDG_CACHE_HOME=" + filepath.Join(out, "preflight-cache"),
		"XDG_DATA_HOME=" + filepath.Join(out, "preflight-data"), "POETRY_CONFIG_DIR=" + filepath.Join(out, "preflight-poetry-config"),
		"POETRY_KEYRING_ENABLED=false", "NETRC=/dev/null",
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
	lastEditAt                               time.Time
	quietChecks                              int
	progressVetoes                           int
	evidenceSeen                             map[string]bool
	before                                   map[string]string
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
	output, err := cmd.CombinedOutput()
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

func (p *m9Probe) venv(path string, dependency bool) string {
	p.t.Helper()
	m9Exec(p.t, p.ctx, p.root, p.env, filepath.Join(p.tools, "bin", "python"), "-I", "-m", "venv", "--without-pip", path)
	if dependency {
		site := filepath.Join(path, "lib", "python3.12", "site-packages")
		m9Write(p.t, filepath.Join(site, "m9_fixture_dep", "__init__.py"), "VALUE: int = 42\n")
		m9Write(p.t, filepath.Join(site, "m9_fixture_dep", "py.typed"), "")
		m9Write(p.t, filepath.Join(site, "m9_fixture_dep-1.0.dist-info", "METADATA"), "Metadata-Version: 2.1\nName: m9-fixture-dep\nVersion: 1.0\n")
	}
	return filepath.Join(path, "bin", "python")
}

func (p *m9Probe) fixture() {
	m9Write(p.t, filepath.Join(p.dir, "lsp.jsonl"), "")
	p.root = filepath.Join(p.dir, "root")
	m9Write(p.t, filepath.Join(p.root, ".huyang.toml"), "# Explicit empty pipeline: no automatic project commands.\n")
	m9Write(p.t, filepath.Join(p.root, "pyproject.toml"), "[project]\nname = \"m9-probe\"\nversion = \"1.0.0\"\nrequires-python = \">=3.12,<3.13\"\n")
	m9JSON(p.t, filepath.Join(p.dir, "fixture-preparation-hashes.json"), m9Hashes(p.t, p.root))
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
		"HOME=" + os.Getenv("HOME"), "LANG=C.UTF-8", "PATH=" + bin, "TMPDIR=" + os.Getenv("TMPDIR"),
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
	for _, name := range []string{"config", "data", "cache", "xdg-state", "poetry-config", "poetry-cache", "poetry-data", "run"} {
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
			version = "999.999.999"
		}
		m9Write(p.t, filepath.Join(p.root, ".python-version"), version+"\n")
	}
	if p.spec.name == "poetry_external" {
		m9Write(p.t, filepath.Join(p.root, "poetry.toml"), fmt.Sprintf("[virtualenvs]\nin-project = false\npath = %q\n[virtualenvs.options]\nno-pip = true\nalways-copy = false\n", filepath.Join(p.dir, "poetry-envs")))
		poetry := filepath.Join(p.tools, "bin", "poetry")
		m9Exec(p.t, p.ctx, p.root, p.env, poetry, "--no-plugins", "env", "use", filepath.Join(p.tools, "bin", "python"))
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
	m9Exec(p.t, p.ctx, p.root, p.env, filepath.Join(bin, "git"), "init", "-q")
	p.before = m9Hashes(p.t, p.root)
	m9JSON(p.t, filepath.Join(p.dir, "before-hashes.json"), p.before)
	if p.selected != "" {
		m9JSON(p.t, filepath.Join(p.dir, "before-environment-hashes.json"), m9Hashes(p.t, filepath.Dir(filepath.Dir(p.selected))))
	}
}

const m9NeovimInit = `
vim.opt.runtimepath:prepend(vim.env.M9_LSPCONFIG)
vim.opt.swapfile = false
vim.opt.undofile = false
local function trace(event)
    local sec, usec = vim.uv.gettimeofday()
    if type(sec) == "table" then usec = sec.usec; sec = sec.sec end
    event.unix_us = sec * 1000000 + usec
    local f = assert(io.open(vim.env.M9_TRACE, "a"))
    f:write(vim.json.encode(event), "\n")
    f:close()
end
local settings = { python = { analysis = { typeCheckingMode = "basic", diagnosticMode = "openFilesOnly" } } }
if vim.env.M9_EXPLICIT_PYTHON then settings.python.pythonPath = vim.env.M9_EXPLICIT_PYTHON end
vim.lsp.config("pyright", { cmd = {vim.env.M9_PYRIGHT, "--stdio"}, filetypes = {"python"},
    root_markers = {"pyproject.toml", ".git"}, settings = settings })
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
-- Observe actual server publications before Huyang's own wrapper consumes
-- them. No diagnostic, version, progress event, timing or result is changed.
local publish = vim.lsp.handlers["textDocument/publishDiagnostics"]
vim.lsp.handlers["textDocument/publishDiagnostics"] = function(err, params, ctx, config)
    trace({kind="publish", params=params, client_id=ctx.client_id})
    return publish(err, params, ctx, config)
end
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
	command := exec.CommandContext(p.ctx, filepath.Join(p.tools, "bin", "strace"), "-ff", "-e", "trace=execve", "-s", "2048", "-o", filepath.Join(p.dir, "exec"), p.binary, "serve", "--socket", socket, "--state-dir", filepath.Join(p.dir, "state"))
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
	done := make(chan struct{})
	go func() { _ = command.Wait(); close(done) }()
	p.t.Cleanup(func() {
		_ = unix.Kill(-command.Process.Pid, unix.SIGTERM)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = unix.Kill(-command.Process.Pid, unix.SIGKILL)
			<-done
		}
		_ = log.Close()
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
	p.t.Cleanup(func() {
		_ = in.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
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
	reply := p.wire.call(p.t, name, args)
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

// A real clean publication must prove its version, quiet period and absence
// of stale findings. Until then, a provisional reply is never called clean.
func (p *m9Probe) collectClean() map[string]any {
	p.t.Helper()
	for p.ctx.Err() == nil {
		reply := p.call("diagnostics", map[string]any{"full": true})
		report := m9Report(reply)
		if report["confidence"] == "authoritative" && len(probeFindings(reply, "main.py", "current", "stale")) == 0 {
			return reply
		}
		p.event()
	}
	p.t.Fatal("no proven current clean publication after fixing the real type error")
	return nil
}

func (p *m9Probe) edit(old, new string) map[string]any {
	reply := p.call("edit_apply", map[string]any{"format": false, "operation": map[string]any{"kind": "replace_literal", "path": "main.py", "old": old, "new": new}})
	p.lastEditAt = time.Now()
	data := m9Object(reply["data"])
	if data["canonical_changed"] != true || !strings.HasPrefix(m9Text(data["revision"]), "wsrev_") {
		p.t.Fatalf("edit lacks canonical/revision guard: %+v", reply)
	}
	if len(m9Text(data["document_revision"])) == 0 && len(m9Object(data["document_revisions"])) == 0 {
		p.t.Fatal("edit omitted document guard")
	}
	return reply
}

func (p *m9Probe) run() {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	p.ctx = ctx
	defer cancel()
	p.evidenceSeen = map[string]bool{}
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
		if p.selected != "" {
			m9JSON(p.t, filepath.Join(p.dir, "after-environment-hashes.json"), m9Hashes(p.t, filepath.Dir(filepath.Dir(p.selected))))
		}
		m9JSON(p.t, filepath.Join(p.dir, "coverage.json"), map[string]any{"case": p.spec.name, "known_expected": p.spec.known, "unknown_expected": p.spec.unknown, "selected_expected": p.selected, "natural_late_publication_observed": p.lateSeen, "later_call_updates_observed": p.updatesSeen, "complete_empty_push_quiet_checks": p.quietChecks, "progress_pending_batches_observed": p.progressVetoes, "passed": !p.t.Failed(),
			"limits": []string{"No forced producer delay or synthetic progress: timing-path coverage is reported, not assumed.", "Pyright may execute the chosen interpreter. Explicit project config is selection ownership, not dependency verification."}})
	})
	p.fixture()
	original, err := os.ReadFile(filepath.Join(p.root, "main.py"))
	if err != nil {
		p.t.Fatal(err)
	}

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
	if p.spec.known {
		p.edit("value: int = \"bad\"", "value: int = 1")
		p.inspectEvidence(p.collectClean())
	}
	p.interpreterAndExecTrace()
	// Restore only our intentional source edits; before/after hashes expose
	// any environment/config mutation caused by real discovery or providers.
	m9Write(p.t, filepath.Join(p.root, "main.py"), string(original))
	if !maps.Equal(p.before, m9Hashes(p.t, p.root)) {
		p.t.Error("canonical fixture changed beyond intentional main.py edits")
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
	body = []byte(strings.Replace(string(body), "marker two", "marker three", 1))
	text = call("edit_apply", map[string]any{"format": false, "operation": map[string]any{"kind": "create_file", "path": "main.py", "replace": true, "revision_id": revision, "content": string(body)}})
	m9Write(p.t, filepath.Join(p.dir, "plain-edit.txt"), text)
	if !strings.Contains(text, "revision_id=docrev_") || !strings.Contains(text, "wsrev_") {
		p.t.Fatal("plain edit lost follow-up guards")
	}
	p.collect()
	text = call("diagnostics", map[string]any{"full": true})
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
	if !strings.Contains(string(encoded), "reportAssignmentType") && !strings.Contains(string(encoded), "provisional") {
		p.t.Fatal("sandbox lost real type error without disclosing provisional coverage")
	}
	if !p.spec.relativeConfig && (strings.Contains(string(encoded), "reportMissingImports") || strings.Contains(string(encoded), "python_environment_unresolved")) {
		p.t.Fatal("automatic sandbox environment lost canonical dependency")
	}
	p.call("change_plan", map[string]any{"action": "discard", "plan_id": plan["plan_id"], "plan_revision": plan["plan_revision"]})
	// Actual selected settings and publication paths (including the
	// sandbox-relative explicit-config outcome) are retained in lsp.jsonl.
}

func (p *m9Probe) interpreterAndExecTrace() {
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
		if event["kind"] == "publish" {
			params := m9Object(event["params"])
			if strings.HasSuffix(m9Text(params["uri"]), "/main.py") {
				count := 0
				for _, raw := range probeSlice(params["diagnostics"]) {
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
		p.t.Fatal("real Pyright did not publish multiple raw missing imports")
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
		if !expect && count != 0 {
			p.t.Fatalf("unexpected %s query, including untrusted/local precedence: %d execs", manager, count)
		}
	}
}

func (p *m9Probe) inspectEvidence(reply map[string]any) {
	found := false
	for _, raw := range probeSlice(m9Object(reply["evidence"])["ids"]) {
		id := m9Text(raw)
		if id == "" {
			continue
		}
		found = true
		if p.evidenceSeen[id] {
			continue
		}
		p.evidenceSeen[id] = true
		evidence := p.call("evidence_get", map[string]any{"evidence_id": id})
		record := m9Object(m9Object(evidence["data"])["evidence"])
		batch := m9Object(record["payload"])
		if text, ok := record["payload"].(string); ok {
			_ = json.Unmarshal([]byte(text), &batch)
		}
		kind := m9Text(batch["kind"])
		if kind != "lsp_push" && kind != "lsp_pull" {
			continue
		}
		if batch["progress_pending"] == true {
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
		if batch["complete"] == true {
			version, vok := batch["document_version"].(float64)
			expected, eok := batch["expected_version"].(float64)
			if !vok || !eok || version < expected || batch["progress_pending"] == true {
				p.t.Fatalf("false complete version/progress evidence: %+v", batch)
			}
			if batch["reason"] == "python_environment_unresolved" {
				p.t.Fatal("grouped evidence claimed complete")
			}
			// Quiet is asserted only for actual complete empty pushes. Pulls
			// and positive pushes do not need an empty-publication quiet wait.
			if kind == "lsp_push" && len(probeSlice(batch["findings"])) == 0 {
				p.assertQuiet(batch)
				p.quietChecks++
			}
		}
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

func m9Hashes(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		var body []byte
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			body = []byte("symlink:" + target)
		} else {
			body, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		sum := sha256.Sum256(body)
		result[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Error(err)
	}
	return result
}

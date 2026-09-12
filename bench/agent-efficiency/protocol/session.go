package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pkoukk/tiktoken-go"
)

// session owns the private Huyang service, the MCP client that talks to it
// through the stdio adapter, and the recorded calls.
type session struct {
	root       string // repository root
	work       string // temporary directory holding fixtures, state and config
	encoder    *tiktoken.Tiktoken
	serve      *exec.Cmd
	client     *mcp.ClientSession
	calls      []Call
	notes      []string
	features   map[string]bool
	workspaces map[string]string
	pipelines  map[string]bool
	// revisions holds the latest workspace revision each language's calls
	// reported, which verify_run has to name exactly.
	revisions map[string]string
	version   string
	// modelFamily selects which pristine copy the modelled calls mutate.
	modelFamily string
}

// fixtureDir is the live copy of one language fixture the Huyang calls
// mutate; modelDir is the pristine copy the modelled families work from.
func (s *session) fixtureDir(language string) string {
	return filepath.Join(s.work, "fixtures", language)
}
func (s *session) modelDir(language string) string {
	return filepath.Join(s.work, "model", language, s.modelFamily)
}

func startSession(root string, encoder *tiktoken.Tiktoken) (*session, error) {
	work, err := os.MkdirTemp("", "huyang-bench-")
	if err != nil {
		return nil, err
	}
	s := &session{root: root, work: work, encoder: encoder, features: map[string]bool{}, workspaces: map[string]string{}, pipelines: map[string]bool{}, revisions: map[string]string{}}
	for _, language := range []string{"go", "python"} {
		source := filepath.Join(root, "bench", "agent-efficiency", "fixtures", language)
		for _, target := range []string{s.fixtureDir(language), filepath.Join(s.work, "model", language, "builtin"), filepath.Join(s.work, "model", language, "bash")} {
			if err := copyTree(source, target); err != nil {
				return nil, err
			}
		}
	}
	if err := s.writeTrust(); err != nil {
		return nil, err
	}
	if err := s.startService(); err != nil {
		return nil, err
	}
	if err := s.connect(); err != nil {
		return nil, err
	}
	return s, nil
}

// writeTrust trusts the fixture copies so declared or detected commands run.
func (s *session) writeTrust() error {
	dir := filepath.Join(s.work, "config", "huyang")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("[trust]\nroots = [%q, %q]\n", s.fixtureDir("go"), s.fixtureDir("python"))
	return os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o644)
}

func (s *session) environment() []string {
	home, _ := os.UserHomeDir()
	path := os.Getenv("PATH") + ":" + filepath.Join(home, "go", "bin") + ":" + filepath.Join(home, ".local", "share", "nvim", "mason", "bin")
	env := []string{}
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "PATH=") || strings.HasPrefix(entry, "XDG_CONFIG_HOME=") || strings.HasPrefix(entry, "HUYANG_") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "PATH="+path, "XDG_CONFIG_HOME="+filepath.Join(s.work, "config"), "HUYANG_RUNTIME_PATH="+s.root)
}

func (s *session) socket() string { return filepath.Join(s.work, "control.sock") }

func (s *session) startService() error {
	binary := filepath.Join(s.root, "bin", "huyang")
	if _, err := os.Stat(binary); err != nil {
		return fmt.Errorf("build bin/huyang first: %w", err)
	}
	s.serve = exec.Command(binary, "serve", "--socket", s.socket(), "--state-dir", filepath.Join(s.work, "state"))
	s.serve.Env = s.environment()
	s.serve.Stderr = os.Stderr
	if err := s.serve.Start(); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(s.socket()); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("service socket did not appear")
}

func (s *session) connect() error {
	binary := filepath.Join(s.root, "bin", "huyang")
	adapter := exec.Command(binary, "mcp", "--socket", s.socket(), "--profile", "full")
	adapter.Env = s.environment()
	adapter.Stderr = os.Stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "huyang-bench", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	clientSession, err := client.Connect(ctx, &mcp.CommandTransport{Command: adapter}, nil)
	if err != nil {
		return err
	}
	s.client = clientSession
	return s.detectFeatures(ctx)
}

// detectFeatures reads the catalog so the same program measures the surface
// before and after the usability changes.
func (s *session) detectFeatures(ctx context.Context) error {
	for tool, err := range s.client.Tools(ctx, nil) {
		if err != nil {
			return err
		}
		schema, _ := tool.InputSchema.(map[string]any)
		properties, _ := schema["properties"].(map[string]any)
		switch tool.Name {
		case "edit_apply":
			operation, _ := properties["operation"].(map[string]any)
			operationProperties, _ := operation["properties"].(map[string]any)
			kind, _ := operationProperties["kind"].(map[string]any)
			for _, value := range anySlice(kind["enum"]) {
				s.features["edit_apply."+fmt.Sprint(value)] = true
			}
			_, s.features["edit_apply.verbose"] = properties["verbose"]
		case "read":
			_, s.features["read.targets"] = properties["targets"]
		}
	}
	return nil
}

func anySlice(value any) []any {
	values, _ := value.([]any)
	return values
}

func (s *session) close() {
	if s.client != nil {
		_ = s.client.Close()
	}
	if s.serve != nil && s.serve.Process != nil {
		_ = s.serve.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = s.serve.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = s.serve.Process.Kill()
		}
	}
	_ = os.RemoveAll(s.work)
}

func (s *session) tokens(text string) int {
	return len(s.encoder.Encode(text, nil, nil))
}

// call performs one Huyang call and records its exact cost. It returns the
// structured envelope for the caller to read handles and outcomes from.
func (s *session) call(scenario, language, tool string, arguments map[string]any, note string) map[string]any {
	request, _ := json.Marshal(map[string]any{"name": tool, "arguments": arguments})
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	result, err := s.client.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: arguments})
	elapsed := time.Since(started)
	call := Call{Scenario: scenario, Language: language, Family: "huyang", Tool: tool, Note: note,
		RequestBytes: len(request), RequestTokens: s.tokens(string(request)), Milliseconds: elapsed.Milliseconds()}
	envelope := map[string]any{}
	if err != nil {
		call.Outcome = "transport_error"
		call.ResponseBytes, call.ResponseTokens = len(err.Error()), s.tokens(err.Error())
		s.notes = append(s.notes, fmt.Sprintf("%s/%s %s failed: %s", scenario, language, tool, firstLine(err.Error())))
	} else {
		text := ""
		for _, content := range result.Content {
			if item, ok := content.(*mcp.TextContent); ok {
				text += item.Text
			}
		}
		call.ResponseBytes, call.ResponseTokens = len(text), s.tokens(text)
		if structured, marshalErr := json.Marshal(result.StructuredContent); marshalErr == nil {
			call.StructuredBytes = len(structured)
			_ = json.Unmarshal(structured, &envelope)
		}
		call.Outcome = fmt.Sprint(envelope["outcome"])
		if revision, ok := data(envelope)["revision"].(string); ok {
			s.revisions[language] = revision
		}
		if result.IsError {
			s.notes = append(s.notes, fmt.Sprintf("%s/%s %s returned %s: %s", scenario, language, tool, envelope["code"], envelope["summary"]))
		}
	}
	s.appendCall(call)
	return envelope
}

// appendCall numbers the call within its scenario, language and family.
func (s *session) appendCall(call Call) {
	step := 1
	for _, previous := range s.calls {
		if previous.Scenario == call.Scenario && previous.Language == call.Language && previous.Family == call.Family {
			step++
		}
	}
	call.Step = step
	s.calls = append(s.calls, call)
}

// modelled records a call the agent would make with another tool family; the
// request is what it would send and the response what it would read back.
func (s *session) modelled(scenario, language, family, tool, request, response, note string) {
	s.appendCall(Call{Scenario: scenario, Language: language, Family: family, Tool: tool, Note: note, Modelled: true,
		RequestBytes: len(request), RequestTokens: s.tokens(request), ResponseBytes: len(response), ResponseTokens: s.tokens(response)})
}

// shell runs a command in dir and returns its combined output, for the
// verification steps every family pays for the same way.
func shell(dir string, command string) string {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	output, _ := cmd.CombinedOutput()
	return string(output)
}

func copyTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(source, path)
		if relative == "." {
			return os.MkdirAll(target, 0o755)
		}
		if entry.Name() == "__pycache__" {
			return filepath.SkipDir
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(destination, content, 0o644)
	})
}

// data returns the data object of an envelope.
func data(envelope map[string]any) map[string]any {
	value, _ := envelope["data"].(map[string]any)
	return value
}

func workspaceID(envelope map[string]any) string {
	workspace, _ := envelope["workspace"].(map[string]any)
	return fmt.Sprint(workspace["id"])
}

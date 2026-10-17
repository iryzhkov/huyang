package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLangprobeABCD is intentionally opt-in: it launches its own private service
// and real installed LSPs. It never contacts the user's service or installs tools.
// HUYANG_LANGPROBE_BINARY optionally selects a previously built private binary.
// Otherwise the test builds its own temporary binary. Optional
// HUYANG_LANGPROBE_SOURCE copies the nine existing probe repositories using Git;
// otherwise the test generates tiny portable fixtures in its temporary directory.
// HUYANG_LANGPROBE_OUTPUT retains JSON replies and the per-language observations.
func TestLangprobeABCD(t *testing.T) {
	if os.Getenv("HUYANG_LANGPROBE") != "1" {
		t.Skip("set HUYANG_LANGPROBE=1 for private-service language probes")
	}
	binary := os.Getenv("HUYANG_LANGPROBE_BINARY")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "huyang")
		if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
			t.Fatalf("build private binary: %v %s", err, output)
		}
	}
	binary, _ = filepath.Abs(binary)
	home, _ := os.UserHomeDir()
	t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+filepath.Join(home, ".local/share/nvim/mason/bin"))
	runtimeRoot, _ := filepath.Abs("../..")
	t.Setenv("HUYANG_RUNTIME_PATH", runtimeRoot)
	runRoot := t.TempDir()
	output := os.Getenv("HUYANG_LANGPROBE_OUTPUT")
	if output == "" {
		output = runRoot
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(output, "sock")
	if len(socket) > 100 {
		socket = filepath.Join(runRoot, "sock")
	}
	if configured := os.Getenv("HUYANG_LANGPROBE_SOCKET"); configured != "" {
		socket = configured
	}
	stateDir := os.Getenv("HUYANG_LANGPROBE_STATE_DIR")
	if stateDir == "" {
		stateDir = filepath.Join(output, "state")
	}
	service := exec.Command(binary, "serve", "--socket", socket, "--state-dir", stateDir)
	logFile, err := os.Create(filepath.Join(output, "service.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	service.Stdout, service.Stderr = logFile, logFile
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = service.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = service.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = service.Process.Kill()
			<-done
		}
	})
	for start := time.Now(); ; {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if time.Since(start) > 10*time.Second {
			t.Fatal("private socket did not appear")
		}
		time.Sleep(25 * time.Millisecond)
	}

	cases := []struct {
		name, server, path, other, before, broken, initial, otherContent string
		markers                                                          map[string]string
	}{
		{"go", "gopls", "main.go", "other.go", "return a + b", "return \"bad\"", "package main\nfunc add(a,b int) int { return a + b }\nfunc main() {}\n// hi, \n", "package main\n// header one\nvar other int = \"bad\"\n", map[string]string{"go.mod": "module example.com/probe\n\ngo 1.25\n"}},
		{"rust", "rust-analyzer", "src/main.rs", "src/bin/other.rs", "a + b", "\"bad\"", "fn add(a:i32,b:i32)->i32 { a + b }\nfn main(){println!(\"{}\",add(1,2));}\n// hi, \n", "// header one\nfn main(){let x:i32=\"bad\";println!(\"{}\",x);}\n", map[string]string{"Cargo.toml": "[package]\nname=\"probe\"\nversion=\"0.1.0\"\nedition=\"2021\"\n"}},
		{"python", "pyright-langserver", "main.py", "other.py", "return a + b", "return \"bad\"", "def add(a: int, b: int) -> int:\n    return a + b\n# hi, \n", "# header one\nx: int = \"bad\"\n", nil},
		{"typescript", "typescript-language-server", "src/main.ts", "src/other.ts", "return a + b;", "return \"bad\";", "export function add(a:number,b:number):number { return a + b; }\n// hi, \n", "// header one\nconst x:number=\"bad\";export {x};\n", map[string]string{"tsconfig.json": "{\"compilerOptions\":{\"strict\":true}}\n", "package.json": "{\"name\":\"probe\",\"version\":\"1.0.0\"}\n"}},
		{"javascript", "typescript-language-server", "main.js", "other.js", "return a + b;", "return \"bad\";", "// @ts-check\n/** @param {number} a @param {number} b @returns {number} */\nfunction add(a,b){return a + b;}\n// hi, \n", "// @ts-check\n// header one\n/** @type {number} */\nconst x=\"bad\";export {x};\n", map[string]string{"package.json": "{\"name\":\"probe\",\"version\":\"1.0.0\"}\n"}},
		{"java", "jdtls", "src/main/java/probe/Main.java", "src/main/java/probe/Other.java", "return a + b;", "return \"bad\";", "package probe;\npublic class Main { static int add(int a,int b){return a + b;} }\n// hi, \n", "package probe;\n// header one\npublic class Other { int x=\"bad\"; }\n", map[string]string{"pom.xml": "<project><modelVersion>4.0.0</modelVersion><groupId>probe</groupId><artifactId>probe</artifactId><version>1</version><properties><maven.compiler.source>21</maven.compiler.source><maven.compiler.target>21</maven.compiler.target></properties></project>\n"}},
		{"cpp", "clangd", "main.cpp", "other.cpp", "return a + b;", "return missing_symbol;", "int add(int a,int b){return a + b;}\nint main(){return add(1,2);}\n// hi, \n", "// header one\nint other_x=\"bad\";\n", map[string]string{"compile_flags.txt": "-std=c++17\n"}},
		{"c", "clangd", "main.c", "other.c", "return a + b;", "return missing_symbol;", "int add(int a,int b){return a + b;}\nint main(void){return add(1,2);}\n// hi, \n", "// header one\nint other_x=\"bad\";\n", nil},
		{"bash", "bash-language-server", "main.sh", "other.sh", "echo $(( $1 + $2 ))", "echo $undefined_one", "#!/usr/bin/env bash\nadd() {\n  echo $(( $1 + $2 ))\n}\nadd 1 2\n# hi, \n", "#!/usr/bin/env bash\n# header one\necho $undefined_two\n", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if filter := os.Getenv("HUYANG_LANGPROBE_LANGUAGES"); filter != "" && !strings.Contains(","+filter+",", ","+tc.name+",") {
				t.Skip("language filter")
			}
			if _, err := exec.LookPath(tc.server); err != nil {
				t.Skipf("%s not installed", tc.server)
			}
			root := filepath.Join(output, tc.name)
			if source := os.Getenv("HUYANG_LANGPROBE_SOURCE"); source != "" {
				if _, err := os.Stat(filepath.Join(root, ".git")); os.IsNotExist(err) {
					cmd := exec.Command("git", "clone", "--local", "--no-hardlinks", filepath.Join(source, tc.name), root)
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("copy probe: %v %s", err, out)
					}
				}
				if out, err := exec.Command("git", "-C", root, "reset", "--hard", "HEAD").CombinedOutput(); err != nil {
					t.Fatalf("reset copy: %v %s", err, out)
				}
			} else {
				write := func(path, content string) {
					t.Helper()
					path = filepath.Join(root, path)
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(content), 0600); err != nil {
						t.Fatal(err)
					}
				}
				write(tc.path, tc.initial)
				write(tc.other, tc.otherContent)
				for p, c := range tc.markers {
					write(p, c)
				}
				for _, args := range [][]string{{"init"}, {"add", "."}, {"-c", "user.name=Langprobe", "-c", "user.email=probe@localhost", "commit", "-m", "fixture"}} {
					cmd := exec.Command("git", args...)
					cmd.Dir = root
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("git: %v %s", err, out)
					}
				}
			}
			wire := startProbeMCP(t, binary, socket, filepath.Join(output, tc.name+"-replies.jsonl"))
			call := func(name string, args map[string]any) map[string]any {
				args["root"] = root
				return wire.call(t, name, args)
			}
			call("workspace_open", map[string]any{})
			call("read", map[string]any{"target": map[string]any{"path": tc.path}})
			// This is fixture warmup, not a wait added to edit_apply.
			time.Sleep(2 * time.Second)
			original, err := os.ReadFile(filepath.Join(root, tc.path))
			if err != nil {
				t.Fatal(err)
			}
			line := strings.Count(string(original[:strings.Index(string(original), tc.before)]), "\n") + 1
			edit := func(path, old, new string) map[string]any {
				return call("edit_apply", map[string]any{"format": false, "operation": map[string]any{"kind": "replace_literal", "path": path, "old": old, "new": new}})
			}
			started := time.Now()
			a := edit(tc.path, tc.before, tc.broken)
			aMS := time.Since(started).Milliseconds()
			checkMutation := func(reply map[string]any) {
				t.Helper()
				data, _ := reply["data"].(map[string]any)
				if data["canonical_changed"] != true {
					t.Fatalf("edit did not apply: %+v", reply)
				}
			}
			checkMutation(a)
			current := func(reply map[string]any, path string) []map[string]any {
				return probeFindings(reply, path, "current")
			}
			collect := func(path string, wantFinding bool) (map[string]any, bool) {
				var reply map[string]any
				attempts := 8
				if tc.name == "java" {
					attempts = 30
				}
				for i := 0; i < attempts; i++ {
					time.Sleep(750 * time.Millisecond)
					reply = call("diagnostics", map[string]any{"full": true})
					found := len(current(reply, path)) > 0
					if found == wantFinding && (wantFinding || len(probeFindings(reply, path, "stale")) == 0) {
						return reply, true
					}
				}
				return reply, false
			}
			aLate, aFound := collect(tc.path, true)
			aItems := append(probeFindings(a, tc.path, "new"), probeUpdates(a, tc.path, "new")...)
			aReported := len(aItems) > 0
			if !aReported && a["outcome"] != "provisional" {
				t.Errorf("A silently clean: %+v", a)
			}
			if !aFound {
				t.Errorf("A finding never arrived: %+v", aLate)
			}
			correctLine := true
			for _, item := range append(aItems, current(aLate, tc.path)...) {
				if n, ok := item["line"].(float64); ok && int(n) != line {
					correctLine = false
				}
				if f, ok := item["finding"].(map[string]any); ok {
					r, _ := f["range"].(map[string]any)
					if n, ok := r["start_line"].(float64); ok && int(n) != line {
						correctLine = false
					}
				}
			}
			if !correctLine {
				t.Errorf("A line should be %d", line)
			}
			b := edit(tc.path, "hi, ", "hello, ")
			checkMutation(b)
			bLate, bFound := collect(tc.path, true)
			bNew := probeFindings(b, tc.path, "new")
			bResolved := len(probeUpdates(b, tc.path, "resolved")) > 0
			if (aFound && len(bNew) > 0) || bResolved || (!bFound && b["outcome"] != "provisional") {
				t.Errorf("B re-announced or falsely resolved: %+v / %+v", b, bLate)
			}
			c := edit(tc.path, tc.broken, tc.before)
			checkMutation(c)
			cLate, cClean := collect(tc.path, false)
			if !cClean {
				if c["outcome"] != "provisional" {
					t.Errorf("C claimed current clean evidence without resolution: %+v", c)
				}
				t.Logf("C remains unverified: producer has not supplied current-version clean evidence")
			}
			d := edit(tc.other, "header one", "header two")
			checkMutation(d)
			dLate, dFound := collect(tc.other, true)
			dItems := append(probeFindings(d, tc.other, "new"), probeFindings(dLate, tc.other, "new")...)
			labelled := true
			for _, item := range dItems {
				if item["baseline_missing"] != true {
					labelled = false
				}
			}
			if !labelled || !dFound {
				t.Errorf("D first touch claimed new without baseline or missed error: %+v / %+v", d, dLate)
			}
			// Reintroduction after resolution exercises the cached/pull bypass.
			again := edit(tc.path, tc.before, tc.broken)
			checkMutation(again)
			againLate, againFound := collect(tc.path, true)
			if len(probeFindings(again, tc.path, "new")) == 0 && again["outcome"] != "provisional" {
				t.Errorf("reintroduction silently clean: %+v", again)
			}
			if !againFound {
				t.Errorf("reintroduced finding never arrived: %+v", againLate)
			}
			row := map[string]any{"language": tc.name, "A_reported": aReported, "A_late": aFound, "line_correct": correctLine, "B_new": len(bNew), "B_false_resolved": bResolved, "C_clean": cClean, "D_labelled": labelled, "D_found": dFound, "A_ms": aMS, "C_final": cLate, "A": a, "B": b, "C": c, "D": d, "reintroduced": again}
			encoded, _ := json.MarshalIndent(row, "", "  ")
			if err := os.WriteFile(filepath.Join(output, tc.name+"-result.json"), encoded, 0600); err != nil {
				t.Fatal(err)
			}
			t.Logf("A reported=%v late=%v line=%v B new=%d false-resolved=%v C clean=%v D labelled=%v found=%v A=%dms", aReported, aFound, correctLine, len(bNew), bResolved, cClean, labelled, dFound, aMS)
		})
	}
}

type probeMCP struct {
	in  io.WriteCloser
	out *bufio.Scanner
	id  int
	log *os.File
}

func startProbeMCP(t *testing.T, binary, socket, logPath string) *probeMCP {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, binary, "mcp", "--socket", socket, "--profile", "edit")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close(); cancel(); _ = cmd.Wait(); _ = logFile.Close() })
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), 32<<20)
	wire := &probeMCP{in: in, out: scanner, log: logFile}
	wire.rpc(t, "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "langprobe", "version": "2"}})
	_, _ = io.WriteString(in, "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n")
	return wire
}
func (w *probeMCP) rpc(t *testing.T, method string, params any) map[string]any {
	t.Helper()
	w.id++
	encoded, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": w.id, "method": method, "params": params})
	_, _ = w.log.Write(append(encoded, '\n'))
	if _, err := w.in.Write(append(encoded, '\n')); err != nil {
		t.Fatal(err)
	}
	for w.out.Scan() {
		line := append([]byte(nil), w.out.Bytes()...)
		_, _ = w.log.Write(append(line, '\n'))
		var reply map[string]any
		if json.Unmarshal(line, &reply) != nil {
			continue
		}
		if reply["id"] == float64(w.id) {
			if reply["error"] != nil {
				t.Fatalf("RPC: %+v", reply)
			}
			result, _ := reply["result"].(map[string]any)
			return result
		}
	}
	t.Fatalf("MCP closed: %v", w.out.Err())
	return nil
}
func (w *probeMCP) call(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	result := w.rpc(t, "tools/call", map[string]any{"name": name, "arguments": args})
	if structured, ok := result["structuredContent"].(map[string]any); ok {
		return structured
	}
	for _, raw := range probeSlice(result["content"]) {
		item, _ := raw.(map[string]any)
		if body, ok := item["text"].(string); ok {
			var decoded map[string]any
			if json.Unmarshal([]byte(body), &decoded) == nil {
				return decoded
			}
		}
	}
	t.Fatalf("no tool envelope for %s: %+v", name, result)
	return nil
}
func probeSlice(v any) []any { items, _ := v.([]any); return items }
func probeFindings(reply map[string]any, path string, groups ...string) []map[string]any {
	data, _ := reply["data"].(map[string]any)
	var result []map[string]any
	for _, field := range []string{"diagnostic_delta", "diagnostics"} {
		if nested, ok := data[field].(map[string]any); ok {
			data = nested
			break
		}
	}
	for _, group := range groups {
		for _, raw := range probeSlice(data[group]) {
			item, _ := raw.(map[string]any)
			severity, _ := item["severity"].(float64)
			if f, ok := item["finding"].(map[string]any); ok {
				severity, _ = f["severity"].(float64)
			}
			if severity >= 3 {
				continue
			}
			if status, _ := item["status"].(string); status != "" && status != group && !(group == "new" && status == "current") {
				continue
			}
			p := fmt.Sprint(item["path"])
			if p == "<nil>" {
				p = fmt.Sprint(item["document"])
			}
			if p == path || strings.HasSuffix(p, "/"+path) {
				result = append(result, item)
			}
		}
	}
	return result
}
func probeUpdates(reply map[string]any, path, kind string) []map[string]any {
	var result []map[string]any
	for _, raw := range probeSlice(reply["diagnostic_updates"]) {
		item, _ := raw.(map[string]any)
		if severity, _ := item["severity"].(float64); severity >= 3 {
			continue
		}
		p := fmt.Sprint(item["path"])
		if item["kind"] == kind && (p == path || strings.HasSuffix(p, "/"+path)) {
			result = append(result, item)
		}
	}
	return result
}

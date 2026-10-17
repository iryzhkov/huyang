package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func pythonFixtureFile(t *testing.T, root, name string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# fixture; never executed\n"), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func pythonTrustFixture(t *testing.T, roots ...string) string {
	t.Helper()
	config := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(config, []byte("[trust]\nroots = []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, root := range roots {
		if _, _, err := TrustRoot(config, root); err != nil {
			t.Fatal(err)
		}
	}
	return config
}

func TestProjectPythonPrecedence(t *testing.T) {
	root := t.TempDir()
	pythonFixtureFile(t, root, "uv.lock", 0644)
	pythonFixtureFile(t, root, "poetry.lock", 0644)
	active := t.TempDir()
	activePython := pythonFixtureFile(t, active, "bin/python", 0755)
	discovery := pythonDiscovery{active: active, userConfig: pythonTrustFixture(t),
		lookPath: func(string) (string, error) { t.Fatal("local discovery ran a manager"); return "", nil },
		run: func(context.Context, string, string, ...string) (string, error) {
			t.Fatal("executed a manager")
			return "", nil
		},
	}
	if got := resolveProjectPython(context.Background(), root, discovery); got.Interpreter != activePython {
		t.Fatalf("active environment = %#v", got)
	}
	legacy := pythonFixtureFile(t, root, "venv/bin/python", 0755)
	if got := resolveProjectPython(context.Background(), root, discovery); got.Interpreter != legacy {
		t.Fatalf("legacy venv = %#v", got)
	}
	preferred := pythonFixtureFile(t, root, ".venv/bin/python", 0755)
	if got := resolveProjectPython(context.Background(), root, discovery); got.Interpreter != preferred {
		t.Fatalf("preferred venv = %#v", got)
	}
	if environment := pythonEnvironment(root); environment.venvBin != filepath.Dir(preferred) {
		t.Fatalf("pipeline selector diverged: %#v", environment)
	}
}

func TestProjectPythonManagerQueries(t *testing.T) {
	for _, manager := range []string{"uv", "poetry"} {
		t.Run(manager, func(t *testing.T) {
			root, tools := t.TempDir(), t.TempDir()
			pythonFixtureFile(t, root, manager+".lock", 0644)
			command := pythonFixtureFile(t, tools, manager, 0755)
			base := pythonFixtureFile(t, tools, "base/bin/python", 0755)
			python := filepath.Join(tools, "env", "bin", "python")
			pythonFixtureFile(t, tools, "env/pyvenv.cfg", 0644)
			if err := os.MkdirAll(filepath.Dir(python), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(base, python); err != nil {
				t.Fatal(err)
			}
			wantArgs := []string{"--offline", "python", "find", "--no-python-downloads"}
			wantTimeout := 2 * time.Second
			if manager == "poetry" {
				wantArgs = []string{"--no-plugins", "env", "info", "--executable"}
				wantTimeout = 5 * time.Second
			}
			var queryStarted time.Time
			queried := false
			discovery := pythonDiscovery{
				userConfig: pythonTrustFixture(t, root),
				lookPath: func(name string) (string, error) {
					if name != manager {
						t.Fatalf("manager = %s", name)
					}
					return command, nil
				},
				run: func(ctx context.Context, cwd, executable string, args ...string) (string, error) {
					queried = true
					deadline, ok := ctx.Deadline()
					if !ok {
						t.Fatal("unbounded discovery")
					}
					// Bracket context creation without sleeping or waiting for expiry.
					if deadline.Before(queryStarted.Add(wantTimeout)) || deadline.After(time.Now().Add(wantTimeout)) {
						t.Fatalf("%s query deadline = %v, want creation time + %v", manager, deadline, wantTimeout)
					}
					if cwd != root || executable != command || !reflect.DeepEqual(args, wantArgs) {
						t.Fatalf("query = %s %s %v", cwd, executable, args)
					}
					return python + "\n", nil
				},
			}
			queryStarted = time.Now()
			got := resolveProjectPython(context.Background(), root, discovery)
			if !queried || got.Interpreter != python || got.Source != manager || got.Reason != "" {
				t.Fatalf("resolved = %#v", got)
			}
		})
	}
}

func TestProjectPythonManagerCallerContext(t *testing.T) {
	for _, manager := range []string{"uv", "poetry"} {
		t.Run(manager, func(t *testing.T) {
			for _, state := range []string{"earlier_deadline", "canceled"} {
				t.Run(state, func(t *testing.T) {
					root, tools := t.TempDir(), t.TempDir()
					pythonFixtureFile(t, root, manager+".lock", 0644)
					command := pythonFixtureFile(t, tools, manager, 0755)
					config := pythonTrustFixture(t, root)

					var caller context.Context
					var cancel context.CancelFunc
					wantErr := context.DeadlineExceeded
					if state == "earlier_deadline" {
						caller, cancel = context.WithDeadline(context.Background(), time.Now().Add(time.Second))
					} else {
						caller, cancel = context.WithCancel(context.Background())
						cancel()
						wantErr = context.Canceled
					}
					defer cancel()
					callerDeadline, hasCallerDeadline := caller.Deadline()
					queried := false
					discovery := pythonDiscovery{
						userConfig: config,
						lookPath: func(string) (string, error) {
							return command, nil
						},
						run: func(ctx context.Context, _, _ string, _ ...string) (string, error) {
							queried = true
							deadline, ok := ctx.Deadline()
							if !ok {
								t.Fatal("unbounded discovery")
							}
							if hasCallerDeadline && !deadline.Equal(callerDeadline) {
								t.Fatalf("query deadline = %v, want caller deadline %v", deadline, callerDeadline)
							}
							if state == "canceled" && !errors.Is(ctx.Err(), context.Canceled) {
								t.Fatalf("query context error = %v, want caller cancellation", ctx.Err())
							}
							// Inject expiry instead of waiting for the earlier deadline.
							return "", wantErr
						},
					}
					got := resolveProjectPython(caller, root, discovery)
					if !queried || got.Interpreter != "" || got.Source != manager || got.Reason != "environment_manager_query_failed" {
						t.Fatalf("resolved = %#v, queried = %v", got, queried)
					}
				})
			}
		})
	}
}

func TestProjectPythonManagerFailures(t *testing.T) {
	for _, failure := range []string{"missing", "project_script", "project_symlink", "query", "relative", "multiline", "not_executable", "absent"} {
		t.Run(failure, func(t *testing.T) {
			root, tools := t.TempDir(), t.TempDir()
			pythonFixtureFile(t, root, "uv.lock", 0644)
			pythonFixtureFile(t, root, "poetry.lock", 0644)
			command := pythonFixtureFile(t, tools, "uv", 0755)
			python := pythonFixtureFile(t, tools, "env/bin/python", 0755)
			if failure == "project_script" {
				command = pythonFixtureFile(t, root, "bin/uv", 0755)
			}
			if failure == "project_symlink" {
				projectScript := pythonFixtureFile(t, root, "bin/uv", 0755)
				if err := os.Remove(command); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(projectScript, command); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "not_executable" {
				if err := os.Chmod(python, 0644); err != nil {
					t.Fatal(err)
				}
			}
			ran := false
			discovery := pythonDiscovery{
				userConfig: pythonTrustFixture(t, root),
				lookPath: func(name string) (string, error) {
					if name != "uv" {
						t.Fatalf("lockfile precedence changed: %s", name)
					}
					if failure == "missing" {
						return "", errors.New("not installed")
					}
					return command, nil
				},
				run: func(context.Context, string, string, ...string) (string, error) {
					ran = true
					switch failure {
					case "query":
						return "", context.DeadlineExceeded
					case "relative":
						return "bin/python", nil
					case "multiline":
						return python + "\n" + python, nil
					case "absent":
						return filepath.Join(tools, "missing"), nil
					default:
						return python, nil
					}
				},
			}
			got := resolveProjectPython(context.Background(), root, discovery)
			if got.Interpreter != "" || got.Reason == "" {
				t.Fatalf("false success: %#v", got)
			}
			if (failure == "missing" || failure == "project_script" || failure == "project_symlink") && ran {
				t.Fatal("unsafe manager query")
			}
		})
	}
}

func TestProjectPythonHonorsProjectConfiguration(t *testing.T) {
	for _, config := range []struct{ name, content string }{
		{"pyrightconfig.json", "{ // JSONC\n \"venvPath\": \"/configured\", \"venv\": \"chosen\" }"},
		{"pyproject.toml", "[tool.pyright]\nvenv = \"chosen\"\n"},
		{"pyproject.toml", "[tool.pyright] # explicit environment\nvenv = \"chosen\"\n"},
		{"pyproject.toml", "tool.pyright.venv = \"chosen\"\n"},
		{"pyproject.toml", "[tool.basedpyright]\nvenvPath = \"/configured\"\n"},
	} {
		t.Run(config.content, func(t *testing.T) {
			root := t.TempDir()
			pythonFixtureFile(t, root, ".venv/bin/python", 0755)
			if err := os.WriteFile(filepath.Join(root, config.name), []byte(config.content), 0644); err != nil {
				t.Fatal(err)
			}
			got := resolveProjectPython(context.Background(), root, pythonDiscovery{})
			if !got.Configured || got.Interpreter != "" || got.Source != "project_config" {
				t.Fatalf("overrode project configuration: %#v", got)
			}
		})
	}
}

func TestProjectPythonUnknownAndInvalidVenv(t *testing.T) {
	root := t.TempDir()
	if got := resolveProjectPython(context.Background(), root, pythonDiscovery{}); got.Interpreter != "" || got.Reason == "" {
		t.Fatalf("unknown = %#v", got)
	}
	pythonFixtureFile(t, root, ".venv/bin/python", 0644)
	if got := resolveProjectPython(context.Background(), root, pythonDiscovery{}); got.Reason != "project_interpreter_not_executable" {
		t.Fatalf("broken venv = %#v", got)
	}
}

func TestPythonManagerOutputIsBounded(t *testing.T) {
	var output strings.Builder
	writer := limitedPythonOutput{writer: &output, remaining: 4}
	if _, err := writer.Write([]byte("path")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("excess")); err == nil {
		t.Fatal("unbounded output")
	}
	if output.String() != "path" {
		t.Fatal("partial oversized output retained")
	}
}

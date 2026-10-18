package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectPythonUntrustedManagerNeverRuns(t *testing.T) {
	for _, manager := range []string{"uv", "poetry"} {
		for _, policy := range []string{"empty", "missing", "malformed", "unreadable", "other_root", "sandbox_only", "symlink_escape"} {
			t.Run(manager+"/"+policy, func(t *testing.T) {
				root := t.TempDir()
				pythonFixtureFile(t, root, manager+".lock", 0644)
				config := pythonTrustFixture(t)
				switch policy {
				case "missing":
					config = filepath.Join(t.TempDir(), "missing.toml")
				case "malformed":
					if err := os.WriteFile(config, []byte("[trust\n"), 0600); err != nil {
						t.Fatal(err)
					}
				case "unreadable":
					config = t.TempDir() // a directory is a policy read error even as root
				case "other_root", "sandbox_only":
					config = pythonTrustFixture(t, t.TempDir())
				case "symlink_escape":
					trusted := t.TempDir()
					config = pythonTrustFixture(t, trusted)
					alias := filepath.Join(trusted, "linked-project")
					if err := os.Symlink(root, alias); err != nil {
						t.Fatal(err)
					}
					root = alias
				}
				discovery := pythonDiscovery{userConfig: config,
					lookPath: func(string) (string, error) { t.Fatal("untrusted manager lookup"); return "", nil },
					run: func(context.Context, string, string, ...string) (string, error) {
						t.Fatal("untrusted manager execution")
						return "", nil
					},
				}
				got := resolveProjectPython(context.Background(), root, discovery)
				if got.Interpreter != "" || got.Configured || got.Source != manager || got.Reason != "environment_manager_untrusted" {
					t.Fatalf("untrusted environment = %#v", got)
				}
			})
		}
	}
}

func TestProjectPythonTrustedManagerRejectsBaseInterpreter(t *testing.T) {
	for _, manager := range []string{"uv", "poetry"} {
		for _, marker := range []string{"absent", "directory"} {
			t.Run(manager+"/"+marker, func(t *testing.T) {
				root, tools := t.TempDir(), t.TempDir()
				pythonFixtureFile(t, root, manager+".lock", 0644)
				command := pythonFixtureFile(t, tools, manager, 0755)
				base := pythonFixtureFile(t, tools, "base/bin/python", 0755)
				if marker == "directory" {
					if err := os.Mkdir(filepath.Join(tools, "base", "pyvenv.cfg"), 0755); err != nil {
						t.Fatal(err)
					}
				}
				queried := false
				discovery := pythonDiscovery{userConfig: pythonTrustFixture(t, root),
					lookPath: func(string) (string, error) { return command, nil },
					run:      func(context.Context, string, string, ...string) (string, error) { queried = true; return base, nil },
				}
				got := resolveProjectPython(context.Background(), root, discovery)
				if !queried || got.Interpreter != "" || got.Configured || got.Source != manager || got.Reason != "environment_manager_not_project_environment" {
					t.Fatalf("base interpreter got project authority: %#v", got)
				}
			})
		}
	}
}

func TestProjectPythonManagerTrustUsesPipelineRootMatching(t *testing.T) {
	for _, kind := range []string{"parent", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "project")
			pythonFixtureFile(t, root, "poetry.lock", 0644)
			config := pythonTrustFixture(t, parent)
			if kind == "symlink" {
				alias := filepath.Join(t.TempDir(), "canonical-alias")
				if err := os.Symlink(root, alias); err != nil {
					t.Fatal(err)
				}
				config = pythonTrustFixture(t, alias)
			}
			pipeline, err := LoadPipelinePolicy(root, config)
			if err != nil || !pipeline.Trusted {
				t.Fatalf("pipeline fixture trust = %v, %v", pipeline.Trusted, err)
			}
			tools := t.TempDir()
			command := pythonFixtureFile(t, tools, "poetry", 0755)
			python := pythonFixtureFile(t, tools, "external-env/bin/python", 0755)
			pythonFixtureFile(t, tools, "external-env/pyvenv.cfg", 0644)
			discovery := pythonDiscovery{userConfig: config,
				lookPath: func(string) (string, error) { return command, nil },
				run: func(_ context.Context, cwd, _ string, _ ...string) (string, error) {
					if cwd != root {
						t.Fatalf("manager cwd = %q, want canonical project %q", cwd, root)
					}
					return python, nil
				},
			}
			got := resolveProjectPython(context.Background(), root, discovery)
			if got.Interpreter != python || got.Source != "poetry" || got.Reason != "" {
				t.Fatalf("trusted external Poetry venv = %#v", got)
			}
		})
	}
}

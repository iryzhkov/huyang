package bridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func TestUnavailableDiagnosticReasonSurvivesJSONAndReachesStageCoverage(t *testing.T) {
	var payload providerDiagnosticPayload
	raw := []byte(`{"batches":[{"kind":"unavailable","provider_id":"nvim_lsp","producer":"nvim_lsp","document":"main.go","selected":true,"dimension":"edited_documents","reason":"lsp_not_configured"}]}`)
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Batches) != 1 || payload.Batches[0].Reason != "lsp_not_configured" {
		t.Fatalf("decoded payload=%#v", payload)
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := workspacecore.Open(workspacecore.OpenOptions{
		Kind: workspacecore.KindProject, Root: root, ProviderEpoch: 1, StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := workspace.RecordDiagnosticEvidence(payload.Batches[0])
	if err != nil {
		t.Fatal(err)
	}
	if report.Confidence != workspacecore.ConfidenceUnavailable {
		t.Fatalf("confidence=%s", report.Confidence)
	}
	coverage := report.Coverage["edited_documents"]
	if len(coverage.Reasons) != 1 || coverage.Reasons[0] != "lsp_not_configured" {
		t.Fatalf("report coverage=%#v", coverage)
	}
	if len(report.ProvisionalReasons) != 1 || report.ProvisionalReasons[0] != "lsp_not_configured" {
		t.Fatalf("report reasons=%v", report.ProvisionalReasons)
	}

	stage := diagnosticVerificationStage("wsrev_1", report)
	if stage.Status != workspacecore.VerificationSkipped {
		t.Fatalf("stage status=%s", stage.Status)
	}
	if len(stage.Coverage.Skipped) != 1 || stage.Coverage.Skipped[0] != "lsp_not_configured" {
		t.Fatalf("stage coverage=%#v", stage.Coverage)
	}
}

func TestRealLanguageDiagnosticBarriersNeverPromoteIncompleteEvidence(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+filepath.Join(home, ".local", "share", "nvim", "mason", "bin"))
	runtimeRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT99_RUNTIME_PATH", runtimeRoot)
	initFile := filepath.Join(runtimeRoot, "tests", "huyang_diagnostics_init.lua")

	cases := []struct {
		name, file, content string
		markers             map[string]string
	}{
		{"gopls", "main.go", "package main\nfunc main() { missing() }\n", map[string]string{"go.mod": "module example.com/evidence\n\ngo 1.25\n"}},
		{"tsserver", "main.ts", "const value: number = \"bad\";\n", map[string]string{"tsconfig.json": "{\"compilerOptions\":{\"strict\":true}}\n"}},
		{"pyright", "main.py", "value: int = \"bad\"\n", map[string]string{"pyproject.toml": "[project]\nname=\"evidence\"\nversion=\"0\"\n"}},
		{"lua_ls", "main.lua", "local value = unknown_global\n", map[string]string{".luarc.json": "{}\n"}},
		{"bash", "main.sh", "echo $((\n", map[string]string{}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if test.name == "bash" {
				if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for name, content := range test.markers {
				if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(root, test.file)
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			workspace, err := workspacecore.Open(workspacecore.OpenOptions{Kind: workspacecore.KindProject, Root: root, ProviderEpoch: 1, StateDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			backend, err := configuredProviderFactory{backend: "embed"}.Open(providerOpenConfig{Root: root, InitFile: initFile, RuntimePath: runtimeRoot})
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close(context.Background())
			report, err := recordProviderDiagnostics(context.Background(), workspace, backend, []workspacecore.PlanStageFile{{Path: test.file, AfterExists: true}}, "wsrev_1", "tx_real")
			if err != nil {
				t.Fatal(err)
			}
			detail, _ := workspace.Evidence(report.EvidenceIDs[0])
			t.Logf("confidence=%s coverage=%#v new=%d evidence=%s", report.Confidence, report.Coverage, len(report.New), detail.Payload)
			stage := diagnosticVerificationStage("wsrev_1", report)
			switch report.Confidence {
			case workspacecore.ConfidenceAuthoritative, workspacecore.ConfidenceCorroborated:
				if stage.Status != workspacecore.VerificationPassed {
					t.Fatalf("complete evidence not passed: %#v", report)
				}
			case workspacecore.ConfidenceProvisional, workspacecore.ConfidenceUnavailable:
				if stage.Status == workspacecore.VerificationPassed {
					t.Fatalf("incomplete evidence promoted clean: %#v", report)
				}
			default:
				t.Fatalf("unknown confidence: %#v", report)
			}
			if len(report.EvidenceIDs) == 0 {
				t.Fatalf("no evidence provenance: %#v", report)
			}
		})
	}
}

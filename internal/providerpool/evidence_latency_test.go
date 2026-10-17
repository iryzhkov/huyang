package providerpool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/iryzhkov/huyang/internal/provider/embed"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// This measures the whole synchronous pre-edit hook: real kernel RPC, payload
// decoding, document snapshot, ledger pruning/serialization/atomic persistence.
// The kernel returns a deterministic LSP payload; no live analysis wait is timed.
func TestDiagnosticBaselineTotalLatencyPopulatedLedger(t *testing.T) {
	if os.Getenv("HUYANG_BASELINE_MEASURE") != "1" {
		t.Skip("set HUYANG_BASELINE_MEASURE=1 for disk-backed baseline measurement")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{0, 1000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			root := t.TempDir()
			state := t.TempDir()
			path := filepath.Join(root, "main.py")
			if err := os.WriteFile(path, []byte("x = 1\n"), 0600); err != nil {
				t.Fatal(err)
			}
			w, err := workspacecore.Open(workspacecore.OpenOptions{Kind: workspacecore.KindProject, Root: root, ProviderEpoch: 1, StateDir: state})
			if err != nil {
				t.Fatal(err)
			}
			v := int64(1)
			batch := workspacecore.DiagnosticBatch{Kind: workspacecore.EvidencePush, ProviderID: "measurement#1", Producer: "measurement", Document: filepath.Join(root, "populated.py"), Complete: true, Selected: true, DocumentVersion: &v, ExpectedVersion: &v}
			for i := 0; i < size; i++ {
				batch.Findings = append(batch.Findings, workspacecore.DiagnosticFinding{Message: fmt.Sprintf("retained diagnostic %d", i), Severity: 1})
			}
			if _, err := w.RecordDiagnosticEvidence(batch); err != nil {
				t.Fatal(err)
			}
			batch.Document = path
			batch.Findings = []workspacecore.DiagnosticFinding{{Message: "baseline finding", Severity: 1}}
			encoded, _ := json.Marshal(batch)
			quoted, _ := json.Marshal(string(encoded))
			runtime, _ := json.Marshal(repo)
			init := filepath.Join(t.TempDir(), "init.lua")
			script := fmt.Sprintf("vim.opt.rtp:prepend(%s)\nlocal batch=vim.json.decode(%s)\nrequire('huyang.edit').diagnostic_evidence=function(args) assert(args.baseline); return { batches={batch} } end\n", runtime, quoted)
			if err := os.WriteFile(init, []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			backend, err := embed.Open(embed.Config{Root: root, RuntimePath: repo, InitFile: init})
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close(context.Background())
			if err := RecordDiagnosticBaseline(context.Background(), w, backend); err != nil {
				t.Fatal(err)
			}
			const samples = 30
			durations := make([]time.Duration, 0, samples)
			for i := 0; i < samples; i++ {
				started := time.Now()
				if err := RecordDiagnosticBaseline(context.Background(), w, backend); err != nil {
					t.Fatal(err)
				}
				durations = append(durations, time.Since(started))
			}
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			report, err := w.Diagnostics("")
			if err != nil {
				t.Fatal(err)
			}
			if report.CurrentCount != size+1 {
				t.Fatalf("measurement lost populated ledger: %d", report.CurrentCount)
			}
			ledger := filepath.Join(state, "diagnostics", string(w.Identity().ID)+".json")
			info, err := os.Stat(ledger)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("total baseline hook: retained=%d ledger_bytes=%d samples=%d min=%s median=%s p95=%s max=%s", size, info.Size(), samples, durations[0], durations[samples/2], durations[28], durations[29])
		})
	}
}

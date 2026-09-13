package workspace

import "testing"

func TestExecutionMutationPrivacyAndRetention(t *testing.T) {
	trace := ExecutionTrace{ID: "t", Policy: ExecutionTracePolicy{CaptureValues: true}}
	raw := map[string]any{"name": "n", "type": "int", "data_id": "address", "old": "1", "new": "2", "old_known": true, "new_known": true}
	mutation := ExecutionWatchMutation(trace, raw)
	if mutation.ObjectID == "" || mutation.OldHash == "" || mutation.NewHash == "" || mutation.OldHash == mutation.NewHash {
		t.Fatalf("%+v", mutation)
	}
	raw["name"] = "password"
	secret := ExecutionWatchMutation(trace, raw)
	if secret.OldHash != "" || secret.NewHash != "" || secret.Values != "redacted" {
		t.Fatalf("%+v", secret)
	}
	w := traceTestWorkspace(t, t.TempDir(), t.TempDir())
	id, err := w.BeginExecutionTrace(trace)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxExecutionValues+1; i++ {
		if err = w.AppendExecutionTrace(ExecutionTraceEvent{Kind: "stopped", Sequence: i + 1, Mutations: []ExecutionMutation{mutation}}); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := w.ExecutionTrace(id)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range stored.Events {
		count += len(event.Mutations)
	}
	if count != MaxExecutionValues || !stored.Coverage.Capped {
		t.Fatalf("mutations=%d coverage=%+v", count, stored.Coverage)
	}
}

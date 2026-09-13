package workspace

import (
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type executionTraceStore struct {
	mu        sync.Mutex
	entries   map[string]*ExecutionTrace
	order     []string
	active    string
	directory string
	loadError error
}

func (w *Workspace) traceStore() *executionTraceStore {
	w.tracesMu.Lock()
	defer w.tracesMu.Unlock()
	if w.traces == nil {
		directory := ""
		if w.stateDir != "" {
			directory = filepath.Join(w.stateDir, "execution-traces", string(w.Identity().ID))
		}
		w.traces = &executionTraceStore{entries: map[string]*ExecutionTrace{}, directory: directory}
		w.traces.loadError = w.traces.recover()
	}
	return w.traces
}
func (w *Workspace) BeginExecutionTrace(trace ExecutionTrace) (string, error) {
	if !validExecutionTraceMetadata(trace) {
		return "", Codedf("trace_metadata_invalid", "trace metadata exceeds its bounds")
	}
	policy, err := NormalizeExecutionTracePolicy(trace.Policy)
	if err != nil {
		return "", err
	}
	store := w.traceStore()
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.loadError != nil {
		return "", store.loadError
	}
	if store.active != "" {
		return "", Codedf("trace_active", "stop the active traced session before starting another")
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	trace.ID = "trace_" + strings.TrimPrefix(string(id), "ws_")
	trace.WorkspaceID, trace.Epoch = w.Identity().ID, w.Identity().Epoch
	trace.Policy = policy
	trace.Started = time.Now().UTC()
	trace.Finished = nil
	trace.Digest = ""
	trace.Events = []ExecutionTraceEvent{}
	trace.Coverage = ExecutionCoverage{Complete: false, Gaps: []string{"one_execution", "user_visible_stops_only"}, Limits: []string{}}
	trace.Warnings = []string{"Debugger stops perturb timing; event order is not a global happens-before relation.", "Arguments, environment values and debugger output are omitted from trace storage."}
	copy := cloneExecution(trace)
	if err = store.persist(&copy); err != nil {
		return "", err
	}
	store.entries[copy.ID] = &copy
	store.order = append(store.order, copy.ID)
	store.active = copy.ID
	if err = store.prune(); err != nil {
		return "", err
	}
	return copy.ID, nil
}
func (w *Workspace) ExecutionTrace(id string) (ExecutionTrace, error) {
	store := w.traceStore()
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.loadError != nil {
		return ExecutionTrace{}, store.loadError
	}
	if id == "" {
		id = store.active
		if id == "" && len(store.order) > 0 {
			id = store.order[len(store.order)-1]
		}
	}
	trace := store.entries[id]
	if trace == nil {
		return ExecutionTrace{}, Codedf("trace_unknown", "trace is absent, expired or outside this workspace")
	}
	return cloneExecution(*trace), nil
}
func (w *Workspace) ActiveExecutionTrace() string {
	store := w.traceStore()
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.active
}
func (w *Workspace) AppendExecutionTrace(event ExecutionTraceEvent, gaps ...string) error {
	store := w.traceStore()
	store.mu.Lock()
	defer store.mu.Unlock()
	stored := store.entries[store.active]
	if stored == nil {
		return nil
	}
	copy := cloneExecution(*stored)
	trace := &copy
	for _, gap := range gaps {
		if len(trace.Coverage.Gaps) < MaxExecutionTraceRedactions {
			trace.Coverage.Gaps = uniqueSorted(append(trace.Coverage.Gaps, sanitizeText(gap, 128)))
		}
	}
	if event.Kind == "stopped" && event.Sequence > 0 {
		if event.Sequence <= trace.LastStopSequence {
			return store.commitTrace(trace)
		}
		if missed := event.Sequence - trace.LastStopSequence - 1; missed > 0 {
			trace.Dropped += missed
			trace.Coverage.Capped = true
			trace.Coverage.Limits = uniqueSorted(append(trace.Coverage.Limits, "uncollected_stops"))
		}
		trace.LastStopSequence = event.Sequence
	}
	event = cloneExecution(event)
	sanitizeTraceEvent(trace, &event)
	if len(trace.Events) >= trace.Policy.MaxEvents || len(executionJSON(trace))+len(executionJSON(event)) > MaxExecutionTraceBytes-4096 {
		trace.Dropped++
		trace.Coverage.Capped = true
		trace.Coverage.Limits = uniqueSorted(append(trace.Coverage.Limits, "trace_events_or_bytes"))
	} else {
		trace.Events = append(trace.Events, event)
	}
	return store.commitTrace(trace)
}
func (w *Workspace) FinishExecutionTrace(reason string) (string, error) {
	store := w.traceStore()
	store.mu.Lock()
	defer store.mu.Unlock()
	stored := store.entries[store.active]
	if stored == nil {
		return "", nil
	}
	copy := cloneExecution(*stored)
	trace := &copy
	now := time.Now().UTC()
	trace.Finished = &now
	trace.Completion = sanitizeText(reason, 128)
	trace.Digest = hashBytes([]byte(executionJSON(trace)))
	if err := store.commitTrace(trace); err != nil {
		return "", err
	}
	store.active = ""
	return trace.ID, nil
}
func (w *Workspace) SetExecutionTraceTargets(targets []ExecutionTraceTarget) error {
	store := w.traceStore()
	store.mu.Lock()
	defer store.mu.Unlock()
	stored := store.entries[store.active]
	if stored == nil {
		return Codedf("trace_unknown", "no active trace")
	}
	copy := cloneExecution(*stored)
	trace := &copy
	if len(targets) > MaxExecutionTraceTargets {
		return Codedf("trace_policy_invalid", "too many targets")
	}
	trace.Targets = cloneExecution(targets)
	return store.commitTrace(trace)
}

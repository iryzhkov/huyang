package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const planStateVersion = 1

type OperationKind string

const (
	OperationReplaceSymbol   OperationKind = "replace_symbol"
	OperationDeleteSymbol    OperationKind = "delete_symbol"
	OperationInsertBefore    OperationKind = "insert_before"
	OperationInsertAfter     OperationKind = "insert_after"
	OperationReplaceRange    OperationKind = "replace_range"
	OperationCreateFile      OperationKind = "create_file"
	OperationMoveFile        OperationKind = "move_file"
	OperationDeleteFile      OperationKind = "delete_file"
	OperationRenameSymbol    OperationKind = "rename_symbol"
	OperationMoveSymbols     OperationKind = "move_symbols"
	OperationReplaceMatches  OperationKind = "replace_matches"
	OperationApplyCodeAction OperationKind = "apply_code_action"
)

type PlanState string

const (
	PlanOpen        PlanState = "OPEN"
	PlanPreviewed   PlanState = "PREVIEWED"
	PlanPreparing   PlanState = "PREPARING"
	PlanFailed      PlanState = "FAILED"
	PlanProvisional PlanState = "PROVISIONAL"
	PlanReady       PlanState = "READY"
	PlanRollingBack PlanState = "ROLLING_BACK"
	PlanRolledBack  PlanState = "ROLLED_BACK"
	PlanDiscarded   PlanState = "DISCARDED"
)

type PlanTarget struct {
	Handle        HandleID           `json:"handle,omitempty"`
	FileRange     *RangeHandle       `json:"file_range,omitempty"`
	SymbolLocator *PlanSymbolLocator `json:"symbol_locator,omitempty"`
}

type PlanSymbolLocator struct {
	Path     string `json:"path"`
	NamePath string `json:"name_path"`
}

type PlanOperation struct {
	OpID                string        `json:"op_id"`
	Kind                OperationKind `json:"kind"`
	Target              *PlanTarget   `json:"target,omitempty"`
	Content             string        `json:"content,omitempty"`
	Path                string        `json:"path,omitempty"`
	From                string        `json:"from,omitempty"`
	To                  string        `json:"to,omitempty"`
	Revision            RevisionID    `json:"revision_id,omitempty"`
	DestinationRevision RevisionID    `json:"destination_revision_id,omitempty"`
	DependsOn           []string      `json:"depends_on,omitempty"`
}

type PlanEdit struct {
	Mode       string          `json:"mode"`
	Operations []PlanOperation `json:"operations,omitempty"`
	OpIDs      []string        `json:"op_ids,omitempty"`
}

type PlanConflict struct {
	OpID     string       `json:"op_id"`
	Code     ConflictCode `json:"code"`
	Path     string       `json:"path,omitempty"`
	Expected RevisionID   `json:"expected_revision,omitempty"`
	Current  RevisionID   `json:"current_revision,omitempty"`
	Message  string       `json:"message"`
}

type PlanPreview struct {
	PreviewRevision  string         `json:"preview_revision"`
	PlanRevision     uint64         `json:"plan_revision"`
	Outcome          string         `json:"outcome"`
	NormalizedOrder  []string       `json:"normalized_order"`
	AffectedFiles    []string       `json:"affected_files"`
	Diffs            []ExactDiff    `json:"diffs,omitempty"`
	Conflicts        []PlanConflict `json:"conflicts,omitempty"`
	CanonicalChanged bool           `json:"canonical_changed"`
}

type PlanRecord struct {
	PlanID       string           `json:"plan_id"`
	WorkspaceID  ID               `json:"workspace_id"`
	State        PlanState        `json:"state"`
	PlanRevision uint64           `json:"plan_revision"`
	BaseStateSeq uint64           `json:"base_state_seq"`
	Operations   []PlanOperation  `json:"operations"`
	Preview      *PlanPreview     `json:"preview,omitempty"`
	Preparation  *PlanPreparation `json:"preparation,omitempty"`
	Events       []PlanEvent      `json:"events"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
}

type PlanEvent struct {
	Action       string    `json:"action"`
	PlanRevision uint64    `json:"plan_revision"`
	Outcome      string    `json:"outcome"`
	At           time.Time `json:"at"`
}

type persistedPlans struct {
	Version int          `json:"version"`
	Plans   []PlanRecord `json:"plans"`
}

func (w *Workspace) planStatePath() string {
	if w.stateDir == "" {
		return ""
	}
	return filepath.Join(w.stateDir, "plans", string(w.Identity().ID)+".json")
}

func (w *Workspace) loadPlans() error {
	path := w.planStatePath()
	if path == "" {
		return nil
	}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read plans: %w", err)
	}
	var state persistedPlans
	if err := json.Unmarshal(content, &state); err != nil {
		return fmt.Errorf("decode plans: %w", err)
	}
	if state.Version != planStateVersion {
		return fmt.Errorf("unsupported plan state version %d", state.Version)
	}
	w.plansMu.Lock()
	defer w.plansMu.Unlock()
	recovered := false
	for i := range state.Plans {
		plan := clonePlan(state.Plans[i])
		if plan.WorkspaceID != w.Identity().ID {
			return fmt.Errorf("plan %s belongs to workspace %s", plan.PlanID, plan.WorkspaceID)
		}
		switch plan.State {
		case PlanPreparing, PlanReady, PlanProvisional, PlanRollingBack:
			plan.State = PlanFailed
			plan.Preparation = nil
			plan.UpdatedAt = time.Now().UTC()
			plan.Events = append(plan.Events, PlanEvent{
				Action: "provider_restart_restore", PlanRevision: plan.PlanRevision,
				Outcome: "provider_buffers_discarded", At: plan.UpdatedAt,
			})
			recovered = true
		}
		w.plans[plan.PlanID] = plan
	}
	if recovered {
		return w.persistPlansLocked()
	}
	return nil
}

func (w *Workspace) persistPlansLocked() error {
	path := w.planStatePath()
	if path == "" {
		return nil
	}
	plans := make([]PlanRecord, 0, len(w.plans))
	for _, plan := range w.plans {
		plans = append(plans, clonePlan(plan))
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].PlanID < plans[j].PlanID })
	content, err := json.MarshalIndent(persistedPlans{Version: planStateVersion, Plans: plans}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode plans: %w", err)
	}
	content = append(content, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".plans-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func newPlanID() (string, error) {
	value, err := randomOpaque("plan_")
	if err != nil {
		return "", err
	}
	return value, nil
}

func (w *Workspace) CreatePlan(operations []PlanOperation) (PlanRecord, error) {
	normalized, err := w.normalizeOperations(operations)
	if err != nil {
		return PlanRecord{}, err
	}
	id, err := newPlanID()
	if err != nil {
		return PlanRecord{}, err
	}
	now := time.Now().UTC()
	plan := PlanRecord{
		PlanID: id, WorkspaceID: w.Identity().ID, State: PlanOpen, PlanRevision: 1,
		BaseStateSeq: w.Identity().StateSeq, Operations: normalized, CreatedAt: now, UpdatedAt: now,
		Events: []PlanEvent{{Action: "create", PlanRevision: 1, Outcome: "ok", At: now}},
	}
	w.plansMu.Lock()
	defer w.plansMu.Unlock()
	w.plans[id] = plan
	if err := w.persistPlansLocked(); err != nil {
		delete(w.plans, id)
		return PlanRecord{}, err
	}
	return clonePlan(plan), nil
}

func (w *Workspace) InspectPlan(planID string, expected uint64) (PlanRecord, error) {
	w.plansMu.Lock()
	defer w.plansMu.Unlock()
	plan, ok := w.plans[planID]
	if !ok {
		return PlanRecord{}, errors.New("unknown plan")
	}
	if expected == 0 || plan.PlanRevision != expected {
		return PlanRecord{}, fmt.Errorf("plan_revision_changed: expected %d, current %d", expected, plan.PlanRevision)
	}
	plan.UpdatedAt = time.Now().UTC()
	plan.Events = append(plan.Events, PlanEvent{
		Action: "inspect", PlanRevision: plan.PlanRevision, Outcome: "ok", At: plan.UpdatedAt,
	})
	w.plans[planID] = plan
	if err := w.persistPlansLocked(); err != nil {
		return PlanRecord{}, err
	}
	return clonePlan(plan), nil
}

func (w *Workspace) EditPlan(planID string, expected uint64, edit PlanEdit) (PlanRecord, error) {
	w.plansMu.Lock()
	defer w.plansMu.Unlock()
	plan, ok := w.plans[planID]
	if !ok {
		return PlanRecord{}, errors.New("unknown plan")
	}
	if plan.State != PlanOpen && plan.State != PlanPreviewed {
		return PlanRecord{}, errors.New("plan cannot be edited in its current state")
	}
	if expected == 0 || plan.PlanRevision != expected {
		return PlanRecord{}, fmt.Errorf("plan_revision_changed: expected %d, current %d", expected, plan.PlanRevision)
	}
	operations := cloneOperations(plan.Operations)
	switch edit.Mode {
	case "add":
		operations = append(operations, edit.Operations...)
	case "update":
		updates := make(map[string]PlanOperation, len(edit.Operations))
		for _, operation := range edit.Operations {
			updates[operation.OpID] = operation
		}
		for i := range operations {
			if update, found := updates[operations[i].OpID]; found {
				operations[i] = update
				delete(updates, operations[i].OpID)
			}
		}
		if len(updates) != 0 {
			return PlanRecord{}, errors.New("update names an unknown op_id")
		}
	case "remove":
		remove := make(map[string]bool, len(edit.OpIDs))
		for _, id := range edit.OpIDs {
			remove[id] = true
		}
		filtered := operations[:0]
		for _, operation := range operations {
			if !remove[operation.OpID] {
				filtered = append(filtered, operation)
			}
		}
		operations = filtered
	case "reorder":
		if len(edit.OpIDs) != len(operations) {
			return PlanRecord{}, errors.New("reorder must name every op_id exactly once")
		}
		byID := make(map[string]PlanOperation, len(operations))
		for _, operation := range operations {
			byID[operation.OpID] = operation
		}
		reordered := make([]PlanOperation, 0, len(operations))
		for _, id := range edit.OpIDs {
			operation, found := byID[id]
			if !found {
				return PlanRecord{}, errors.New("reorder contains an unknown or duplicate op_id")
			}
			reordered = append(reordered, operation)
			delete(byID, id)
		}
		operations = reordered
	case "replace_all":
		operations = edit.Operations
	default:
		return PlanRecord{}, fmt.Errorf("unknown plan edit mode %q", edit.Mode)
	}
	normalized, err := w.normalizeOperations(operations)
	if err != nil {
		return PlanRecord{}, err
	}
	plan.Operations = normalized
	plan.State = PlanOpen
	plan.PlanRevision++
	plan.Preview = nil
	plan.UpdatedAt = time.Now().UTC()
	plan.Events = append(plan.Events, PlanEvent{
		Action: "edit", PlanRevision: plan.PlanRevision, Outcome: "ok", At: plan.UpdatedAt,
	})
	w.plans[planID] = plan
	if err := w.persistPlansLocked(); err != nil {
		return PlanRecord{}, err
	}
	return clonePlan(plan), nil
}

func (w *Workspace) DiscardPlan(planID string, expected uint64) (PlanRecord, error) {
	w.plansMu.Lock()
	defer w.plansMu.Unlock()
	plan, ok := w.plans[planID]
	if !ok {
		return PlanRecord{}, errors.New("unknown plan")
	}
	if expected == 0 || plan.PlanRevision != expected {
		return PlanRecord{}, fmt.Errorf("plan_revision_changed: expected %d, current %d", expected, plan.PlanRevision)
	}
	plan.State = PlanDiscarded
	plan.UpdatedAt = time.Now().UTC()
	plan.Events = append(plan.Events, PlanEvent{
		Action: "discard", PlanRevision: plan.PlanRevision, Outcome: "ok", At: plan.UpdatedAt,
	})
	w.plans[planID] = plan
	if err := w.persistPlansLocked(); err != nil {
		return PlanRecord{}, err
	}
	return clonePlan(plan), nil
}

func (w *Workspace) PreviewPlan(planID string, expected uint64) (PlanRecord, error) {
	w.plansMu.Lock()
	plan, ok := w.plans[planID]
	w.plansMu.Unlock()
	if !ok {
		return PlanRecord{}, errors.New("unknown plan")
	}
	if plan.State != PlanOpen && plan.State != PlanPreviewed {
		return PlanRecord{}, errors.New("plan cannot be previewed in its current state")
	}
	if expected == 0 || plan.PlanRevision != expected {
		return PlanRecord{}, fmt.Errorf("plan_revision_changed: expected %d, current %d", expected, plan.PlanRevision)
	}
	preview := w.buildPreview(plan)
	w.plansMu.Lock()
	defer w.plansMu.Unlock()
	current, ok := w.plans[planID]
	if !ok || current.PlanRevision != expected ||
		(current.State != PlanOpen && current.State != PlanPreviewed) {
		return PlanRecord{}, errors.New("plan changed while preview was being built")
	}
	current.Preview = &preview
	current.State = PlanPreviewed
	current.UpdatedAt = time.Now().UTC()
	current.Events = append(current.Events, PlanEvent{
		Action: "preview", PlanRevision: current.PlanRevision, Outcome: preview.Outcome, At: current.UpdatedAt,
	})
	w.plans[planID] = current
	if err := w.persistPlansLocked(); err != nil {
		return PlanRecord{}, err
	}
	return clonePlan(current), nil
}

func (w *Workspace) normalizeOperations(operations []PlanOperation) ([]PlanOperation, error) {
	result := cloneOperations(operations)
	seen := make(map[string]bool, len(result))
	for i := range result {
		operation := &result[i]
		operation.OpID = strings.TrimSpace(operation.OpID)
		if operation.OpID == "" {
			return nil, errors.New("every operation requires op_id")
		}
		if seen[operation.OpID] {
			return nil, fmt.Errorf("duplicate op_id %q", operation.OpID)
		}
		seen[operation.OpID] = true
		switch operation.Kind {
		case OperationReplaceSymbol, OperationDeleteSymbol, OperationInsertBefore, OperationInsertAfter, OperationReplaceRange:
			if operation.Target == nil {
				return nil, fmt.Errorf("%s requires target", operation.OpID)
			}
			if operation.Target.FileRange == nil && operation.Target.Handle != "" {
				resolution, err := w.ResolveHandle(operation.Target.Handle)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", operation.OpID, err)
				}
				if resolution.Status == ResolutionConflicted {
					return nil, fmt.Errorf("%s: %s", operation.OpID, resolution.Code)
				}
				handle, err := resolution.RangeHandle()
				if err != nil {
					return nil, fmt.Errorf("%s: %w", operation.OpID, err)
				}
				operation.Target.FileRange = &handle
			}
			if operation.Target.FileRange == nil && operation.Target.SymbolLocator != nil {
				matches, _, err := w.FindSymbols(operation.Target.SymbolLocator.NamePath)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", operation.OpID, err)
				}
				var selected []HandleRecord
				for _, match := range matches {
					if match.Locator.NamePath == operation.Target.SymbolLocator.NamePath &&
						match.Locator.Path == operation.Target.SymbolLocator.Path {
						selected = append(selected, match)
					}
				}
				if len(selected) != 1 {
					return nil, fmt.Errorf("%s: symbol locator resolved to %d declarations", operation.OpID, len(selected))
				}
				resolution, err := w.ResolveHandle(selected[0].Handle)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", operation.OpID, err)
				}
				handle, err := resolution.RangeHandle()
				if err != nil {
					return nil, fmt.Errorf("%s: %w", operation.OpID, err)
				}
				operation.Target.FileRange = &handle
			}
			if operation.Target.FileRange == nil {
				return nil, fmt.Errorf("%s requires a resolvable handle or file_range", operation.OpID)
			}
			if operation.Kind == OperationDeleteSymbol {
				operation.Content = ""
			}
		case OperationCreateFile, OperationDeleteFile:
			if operation.Path == "" || operation.Revision == "" {
				return nil, fmt.Errorf("%s requires path and revision_id", operation.OpID)
			}
		case OperationMoveFile:
			if operation.From == "" || operation.To == "" || operation.Revision == "" || operation.DestinationRevision == "" {
				return nil, fmt.Errorf("%s requires from, to, revision_id, and destination_revision_id", operation.OpID)
			}
		case OperationRenameSymbol, OperationMoveSymbols, OperationReplaceMatches, OperationApplyCodeAction:
			if operation.Target == nil || (operation.Target.Handle == "" && operation.Target.FileRange == nil && operation.Target.SymbolLocator == nil) {
				return nil, fmt.Errorf("%s requires target", operation.OpID)
			}
		default:
			return nil, fmt.Errorf("%s has unknown operation kind %q", operation.OpID, operation.Kind)
		}
		for _, dependency := range operation.DependsOn {
			if dependency == operation.OpID {
				return nil, fmt.Errorf("%s cannot depend on itself", operation.OpID)
			}
		}
	}
	for _, operation := range result {
		for _, dependency := range operation.DependsOn {
			if !seen[dependency] {
				return nil, fmt.Errorf("%s depends on unknown op_id %s", operation.OpID, dependency)
			}
		}
	}
	if _, err := orderOperations(result); err != nil {
		return nil, err
	}
	return result, nil
}

func orderOperations(operations []PlanOperation) ([]PlanOperation, error) {
	byID := make(map[string]PlanOperation, len(operations))
	index := make(map[string]int, len(operations))
	indegree := make(map[string]int, len(operations))
	next := make(map[string][]string, len(operations))
	for i, operation := range operations {
		byID[operation.OpID] = operation
		index[operation.OpID] = i
		indegree[operation.OpID] = 0
	}
	addEdge := func(before, after string) {
		for _, existing := range next[before] {
			if existing == after {
				return
			}
		}
		next[before] = append(next[before], after)
		indegree[after]++
	}
	for _, operation := range operations {
		for _, dependency := range operation.DependsOn {
			addEdge(dependency, operation.OpID)
		}
	}
	for i, left := range operations {
		for j, right := range operations {
			if i == j {
				continue
			}
			leftPath, leftStart, leftEdit := operationRange(left)
			rightPath, rightStart, rightEdit := operationRange(right)
			if leftEdit && rightEdit && leftPath == rightPath && leftStart < rightStart {
				addEdge(right.OpID, left.OpID)
			}
			if left.Kind == OperationCreateFile && left.Path != "" && rightEdit && rightPath == left.Path {
				addEdge(left.OpID, right.OpID)
			}
			if leftEdit && right.Kind == OperationDeleteFile && leftPath == right.Path {
				addEdge(left.OpID, right.OpID)
			}
			if leftEdit && right.Kind == OperationMoveFile && leftPath == right.From {
				addEdge(left.OpID, right.OpID)
			}
		}
	}
	ready := make([]string, 0, len(operations))
	for id, count := range indegree {
		if count == 0 {
			ready = append(ready, id)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return index[ready[i]] < index[ready[j]] })
	ordered := make([]PlanOperation, 0, len(operations))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		ordered = append(ordered, byID[id])
		for _, target := range next[id] {
			indegree[target]--
			if indegree[target] == 0 {
				ready = append(ready, target)
				sort.Slice(ready, func(i, j int) bool { return index[ready[i]] < index[ready[j]] })
			}
		}
	}
	if len(ordered) != len(operations) {
		return nil, errors.New("operation dependency cycle")
	}
	return ordered, nil
}

func operationRange(operation PlanOperation) (string, int, bool) {
	if operation.Target == nil || operation.Target.FileRange == nil {
		return "", 0, false
	}
	return operation.Target.FileRange.Path, operation.Target.FileRange.ByteStart, true
}

func (w *Workspace) buildPreview(plan PlanRecord) PlanPreview {
	ordered, orderErr := orderOperations(plan.Operations)
	preview := PlanPreview{PlanRevision: plan.PlanRevision, Outcome: "ok", CanonicalChanged: false}
	if orderErr != nil {
		preview.Outcome = "conflict"
		preview.Conflicts = append(preview.Conflicts, PlanConflict{Code: "operation_dependency_cycle", Message: orderErr.Error()})
		return finalizePreview(preview)
	}
	for _, operation := range ordered {
		preview.NormalizedOrder = append(preview.NormalizedOrder, operation.OpID)
	}
	contents := make(map[string][]byte)
	exists := make(map[string]bool)
	before := make(map[string][]byte)
	touched := make(map[string]bool)
	conflictFor := func(operation PlanOperation, path string, expected RevisionID, err error) {
		conflict := PlanConflict{OpID: operation.OpID, Code: ConflictDocumentChanged, Path: path, Expected: expected, Message: err.Error()}
		var typed *Conflict
		if errors.As(err, &typed) {
			conflict.Code, conflict.Current = typed.Code, typed.Current
		}
		preview.Conflicts = append(preview.Conflicts, conflict)
	}
	load := func(path string) ([]byte, bool, error) {
		if content, ok := contents[path]; ok {
			return content, exists[path], nil
		}
		read, err := w.Read(path)
		if err != nil {
			snapshot, snapshotErr := w.Refresh(path, ProviderLayer{})
			if snapshotErr == nil && snapshot.Disk.Kind == ObjectMissing {
				contents[path], before[path], exists[path] = nil, nil, false
				return nil, false, nil
			}
			return nil, false, err
		}
		content := append([]byte(nil), read.Content...)
		contents[path], before[path], exists[path] = content, append([]byte(nil), content...), true
		return content, true, nil
	}
	for _, operation := range ordered {
		switch operation.Kind {
		case OperationReplaceSymbol, OperationDeleteSymbol, OperationInsertBefore, OperationInsertAfter, OperationReplaceRange:
			handle := *operation.Target.FileRange
			current, err := w.ValidateMutation(w.Identity().ID, handle.Path, handle.Revision, ProviderLayer{})
			if err != nil {
				conflictFor(operation, handle.Path, handle.Revision, err)
				continue
			}
			content, present, err := load(handle.Path)
			if err != nil || !present {
				if err == nil {
					err = errors.New("target file is missing")
				}
				conflictFor(operation, handle.Path, handle.Revision, err)
				continue
			}
			if handle.ByteStart < 0 || handle.ByteEnd < handle.ByteStart || handle.ByteEnd > len(content) ||
				hashBytes(content[handle.ByteStart:handle.ByteEnd]) != handle.ExpectedSHA256 {
				conflictFor(operation, handle.Path, handle.Revision, &Conflict{Code: ConflictDocumentChanged, Path: handle.Path, Expected: handle.Revision, Current: current.Revision})
				continue
			}
			start, end := handle.ByteStart, handle.ByteEnd
			switch operation.Kind {
			case OperationInsertBefore:
				end = start
			case OperationInsertAfter:
				start = end
			}
			replacement := []byte(operation.Content)
			nextContent := make([]byte, 0, len(content)-(end-start)+len(replacement))
			nextContent = append(nextContent, content[:start]...)
			nextContent = append(nextContent, replacement...)
			nextContent = append(nextContent, content[end:]...)
			contents[handle.Path] = nextContent
			touched[handle.Path] = true
		case OperationCreateFile:
			current, err := w.ValidateMutation(w.Identity().ID, operation.Path, operation.Revision, ProviderLayer{})
			if err != nil {
				conflictFor(operation, operation.Path, operation.Revision, err)
				continue
			}
			if current.Disk.Kind != ObjectMissing {
				conflictFor(operation, operation.Path, operation.Revision, errors.New("create target already exists"))
				continue
			}
			_, _, _ = load(operation.Path)
			contents[operation.Path], exists[operation.Path], touched[operation.Path] = []byte(operation.Content), true, true
		case OperationDeleteFile:
			if _, err := w.ValidateMutation(w.Identity().ID, operation.Path, operation.Revision, ProviderLayer{}); err != nil {
				conflictFor(operation, operation.Path, operation.Revision, err)
				continue
			}
			if _, present, err := load(operation.Path); err != nil || !present {
				if err == nil {
					err = errors.New("delete target is missing")
				}
				conflictFor(operation, operation.Path, operation.Revision, err)
				continue
			}
			contents[operation.Path], exists[operation.Path], touched[operation.Path] = nil, false, true
		case OperationMoveFile:
			if _, err := w.ValidateMutation(w.Identity().ID, operation.From, operation.Revision, ProviderLayer{}); err != nil {
				conflictFor(operation, operation.From, operation.Revision, err)
				continue
			}
			destination, err := w.ValidateMutation(w.Identity().ID, operation.To, operation.DestinationRevision, ProviderLayer{})
			if err != nil {
				conflictFor(operation, operation.To, operation.DestinationRevision, err)
				continue
			}
			if destination.Disk.Kind != ObjectMissing {
				conflictFor(operation, operation.To, operation.DestinationRevision, errors.New("move destination exists"))
				continue
			}
			source, present, err := load(operation.From)
			if err != nil || !present {
				if err == nil {
					err = errors.New("move source is missing")
				}
				conflictFor(operation, operation.From, operation.Revision, err)
				continue
			}
			_, _, _ = load(operation.To)
			contents[operation.To], exists[operation.To], touched[operation.To] = append([]byte(nil), source...), true, true
			contents[operation.From], exists[operation.From], touched[operation.From] = nil, false, true
		default:
			preview.Conflicts = append(preview.Conflicts, PlanConflict{
				OpID: operation.OpID, Code: "operation_requires_later_stage",
				Message: fmt.Sprintf("%s requires provider or result-set validation unavailable before its named stage", operation.Kind),
			})
		}
	}
	if len(preview.Conflicts) > 0 {
		preview.Outcome = "conflict"
		return finalizePreview(preview)
	}
	for path := range touched {
		preview.AffectedFiles = append(preview.AffectedFiles, path)
		if exists[path] {
			preview.Diffs = append(preview.Diffs, exactDiff(path, before[path], contents[path], 0, len(before[path]), contents[path]))
		} else {
			preview.Diffs = append(preview.Diffs, exactDiff(path, before[path], nil, 0, len(before[path]), nil))
		}
	}
	sort.Strings(preview.AffectedFiles)
	sort.Slice(preview.Diffs, func(i, j int) bool { return preview.Diffs[i].Path < preview.Diffs[j].Path })
	return finalizePreview(preview)
}

func finalizePreview(preview PlanPreview) PlanPreview {
	copy := preview
	copy.PreviewRevision = ""
	encoded, _ := json.Marshal(copy)
	sum := sha256.Sum256(encoded)
	preview.PreviewRevision = "preview_" + hex.EncodeToString(sum[:16])
	return preview
}

func cloneOperations(source []PlanOperation) []PlanOperation {
	encoded, _ := json.Marshal(source)
	var clone []PlanOperation
	_ = json.Unmarshal(encoded, &clone)
	if clone == nil {
		clone = []PlanOperation{}
	}
	return clone
}

func clonePlan(source PlanRecord) PlanRecord {
	encoded, _ := json.Marshal(source)
	var clone PlanRecord
	_ = json.Unmarshal(encoded, &clone)
	return clone
}

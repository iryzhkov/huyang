package workspace

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func validExecutionTraceID(id string) bool {
	if len(id) != 38 || !strings.HasPrefix(id, "trace_") {
		return false
	}
	for _, c := range id[6:] {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
func (s *executionTraceStore) persist(trace *ExecutionTrace) error {
	content, err := json.Marshal(trace)
	if err != nil || len(content) > MaxExecutionTraceBytes {
		return Codedf("trace_storage_invalid", "trace exceeds its encoded bound")
	}
	if s.directory == "" {
		return nil
	}
	if err = os.MkdirAll(s.directory, 0700); err != nil {
		return Codedf("trace_storage_failed", "trace directory unavailable")
	}
	if err = atomicWriteFile(filepath.Join(s.directory, trace.ID+".json"), content, 0600); err != nil {
		return Codedf("trace_storage_failed", "trace could not be persisted")
	}
	return nil
}
func (s *executionTraceStore) prune() error {
	for len(s.order) > MaxExecutionTraces {
		id := s.order[0]
		if s.directory != "" {
			if err := os.Remove(filepath.Join(s.directory, id+".json")); err != nil && !os.IsNotExist(err) {
				return Codedf("trace_storage_failed", "old trace could not be pruned")
			}
		}
		delete(s.entries, id)
		s.order = s.order[1:]
	}
	return nil
}
func (s *executionTraceStore) recover() error {
	if s.directory == "" {
		return nil
	}
	directory, err := os.Open(s.directory)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return Codedf("trace_storage_failed", "trace directory unreadable")
	}
	defer directory.Close()
	// A bounded read refuses a corrupt/unbounded directory instead of loading it.
	entries, err := directory.ReadDir(MaxExecutionTraces + 2)
	if err != nil && err != io.EOF {
		return Codedf("trace_storage_failed", "trace directory enumeration failed")
	}
	if len(entries) > MaxExecutionTraces+1 {
		return Codedf("trace_storage_invalid", "trace directory exceeds retention bounds")
	}
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !validExecutionTraceID(id) || entry.Name() != id+".json" || !entry.Type().IsRegular() {
			return Codedf("trace_storage_invalid", "unexpected trace storage entry")
		}
		info, err := entry.Info()
		if err != nil || info.Size() > MaxExecutionTraceBytes || info.Mode().Perm() != 0600 {
			return Codedf("trace_storage_invalid", "trace size or permissions invalid")
		}
		content, err := readBoundedTrace(filepath.Join(s.directory, entry.Name()))
		var trace ExecutionTrace
		if err != nil || json.Unmarshal(content, &trace) != nil || trace.ID != id || len(trace.Events) > MaxExecutionTraceEvents || !s.validRecoveredTrace(trace) {
			return Codedf("trace_storage_invalid", "trace record invalid")
		}
		if trace.Finished == nil {
			now := time.Now().UTC()
			trace.Finished = &now
			trace.Completion = "daemon_restart"
			trace.Coverage.Complete = false
			trace.Coverage.Gaps = uniqueSorted(append(trace.Coverage.Gaps, "capture_interrupted"))
			trace.Digest = hashBytes([]byte(executionJSON(trace)))
			if err = s.persist(&trace); err != nil {
				return err
			}
		}
		s.entries[id] = &trace
		s.order = append(s.order, id)
	}
	sort.Slice(s.order, func(i, j int) bool {
		a, b := s.entries[s.order[i]], s.entries[s.order[j]]
		if a.Started.Equal(b.Started) {
			return a.ID < b.ID
		}
		return a.Started.Before(b.Started)
	})
	return s.prune()
}

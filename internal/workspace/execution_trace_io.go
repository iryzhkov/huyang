package workspace

import (
	"io"
	"os"
)

func readBoundedTrace(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, Codedf("trace_storage_invalid", "trace record type or mode changed")
	}
	content, err := io.ReadAll(io.LimitReader(file, MaxExecutionTraceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > MaxExecutionTraceBytes {
		return nil, Codedf("trace_storage_invalid", "trace grew beyond its byte ceiling")
	}
	return content, nil
}
func (store *executionTraceStore) commitTrace(trace *ExecutionTrace) error {
	if err := store.persist(trace); err != nil {
		return err
	}
	store.entries[trace.ID] = trace
	return nil
}

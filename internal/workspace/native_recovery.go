package workspace

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RecoverNativeJournals resolves the native edit journals (native-*.json)
// that a process killed in the middle of a mutation left in stateDir. The
// service calls it once at startup, after the registry has restored every
// workspace and before it serves a request, so no journal it reads can
// belong to a mutation still in progress.
//
// A mutation writes its journal completely and syncs it before it touches
// the target, and it has not been reported to anyone until the journal is
// gone, so each journal resolves to the state before the mutation:
//
//   - a journal that does not decode was cut short before the target was
//     touched, and is discarded;
//   - a target that still holds the preimage needs nothing, and its journal
//     is cleared;
//   - a target that holds the postimage is put back to the preimage, but
//     only when one of workspaces confines it, because that is what makes a
//     path in a journal safe to write;
//   - anything else, including a postimage outside every workspace, is a
//     conflict: the journal stays and the target is left untouched.
//
// Journals used to be resolved only by Workspace.Recover, which nothing
// called, and which failed on the first journal of another workspace, since
// every workspace shares the one state directory. A journal whose mutation
// failed before writing (a changed precondition) was also never removed.
// Those journals stayed for ever. An error for one journal does not stop the
// others from being resolved.
func RecoverNativeJournals(stateDir string, workspaces []*Workspace) (RecoveryResult, error) {
	var result RecoveryResult
	if stateDir == "" {
		return result, errors.New("native mutation recovery state directory is required")
	}
	entries, err := os.ReadDir(stateDir)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	var failures []error
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "native-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if err := recoverNativeJournal(filepath.Join(stateDir, entry.Name()), workspaces, &result); err != nil {
			failures = append(failures, fmt.Errorf("recovery journal %s: %w", entry.Name(), err))
		}
	}
	sort.Strings(result.Recovered)
	sort.Strings(result.Cleared)
	sort.Strings(result.Conflicts)
	sort.Strings(result.Discarded)
	return result, errors.Join(failures...)
}

func recoverNativeJournal(journalPath string, workspaces []*Workspace, result *RecoveryResult) error {
	data, err := os.ReadFile(journalPath)
	if err != nil {
		return err
	}
	var record journalRecord
	if json.Unmarshal(data, &record) != nil {
		if err := os.Remove(journalPath); err != nil {
			return err
		}
		result.Discarded = append(result.Discarded, filepath.Base(journalPath))
		return nil
	}
	if record.Version != 1 {
		return fmt.Errorf("unsupported version %d", record.Version)
	}
	preimage, preErr := base64.StdEncoding.DecodeString(record.Preimage)
	postimage, postErr := base64.StdEncoding.DecodeString(record.Postimage)
	if preErr != nil || postErr != nil || !filepath.IsAbs(record.Path) {
		return errors.New("invalid payload")
	}
	target, owned := nativeJournalTarget(record.Path, workspaces)
	if stateMatches(target, record.PreExists, preimage) {
		if err := os.Remove(journalPath); err != nil {
			return err
		}
		result.Cleared = append(result.Cleared, target)
		return nil
	}
	if !owned || !stateMatches(target, record.PostExists, postimage) {
		result.Conflicts = append(result.Conflicts, target)
		return nil
	}
	if record.PreExists {
		err = atomicWriteFile(target, preimage, fs.FileMode(record.Mode))
	} else if err = os.Remove(target); err == nil || errors.Is(err, os.ErrNotExist) {
		err = syncDirectory(filepath.Dir(target))
	}
	if err != nil {
		return err
	}
	if err := os.Remove(journalPath); err != nil {
		return err
	}
	result.Recovered = append(result.Recovered, target)
	return nil
}

// nativeJournalTarget confines a journal's path to the first workspace that
// owns it. An unowned path is returned cleaned, for reading only.
func nativeJournalTarget(path string, workspaces []*Workspace) (string, bool) {
	for _, workspace := range workspaces {
		if workspace == nil {
			continue
		}
		if target, err := workspace.confinedPath(path); err == nil {
			return target, true
		}
	}
	return filepath.Clean(path), false
}

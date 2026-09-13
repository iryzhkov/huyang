package workspace

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fixtureSectioner struct {
	fail bool
}

func (f fixtureSectioner) Sections(_ string, content []byte) ([]Section, error) {
	if f.fail {
		return nil, errors.New("parser unavailable")
	}
	end := len(content)
	if newline := strings.IndexByte(string(content), '\n'); newline >= 0 {
		end = newline
	}
	return []Section{{Name: "heading", Kind: "section", ByteStart: 0, ByteEnd: end}}, nil
}

func newNativeWorkspace(t *testing.T, kind Kind, root string, files []string, limits Limits) *Workspace {
	t.Helper()
	ws, err := Open(OpenOptions{
		Kind: kind, Root: root, Files: files, ProviderEpoch: 1,
		StateDir: filepath.Join(t.TempDir(), "state"), Limits: limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

func TestDocumentWorkspaceAllowlistNeverScansSiblings(t *testing.T) {
	root := t.TempDir()
	formats := map[string]string{
		"note.md":       "# heading\nneedle\n",
		"data.json":     "{\"needle\":true}\n",
		"config.toml":   "name = \"needle\"\n",
		"config.yaml":   "name: needle\n",
		"Dockerfile":    "FROM needle\n",
		"plain.unknown": "needle\n",
	}
	var allowlist []string
	for name, content := range formats {
		path := filepath.Join(root, name)
		writeFile(t, path, content)
		allowlist = append(allowlist, path)
	}
	sibling := filepath.Join(root, "private-sibling.txt")
	writeFile(t, sibling, "needle must not be observed")
	nestedSibling := filepath.Join(root, "unrelated", "nested.txt")
	if err := os.MkdirAll(filepath.Dir(nestedSibling), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, nestedSibling, "needle must not be observed")

	ws := newNativeWorkspace(t, KindDocuments, root, allowlist, Limits{})
	orientation, err := ws.Orient()
	if err != nil {
		t.Fatal(err)
	}
	if len(orientation.Entries) != len(formats) || !orientation.Coverage.Complete {
		t.Fatalf("document orientation = %+v", orientation)
	}
	for _, entry := range orientation.Entries {
		if strings.Contains(entry.Path, "sibling") || strings.Contains(entry.Path, "unrelated") {
			t.Fatalf("document workspace scanned sibling %q", entry.Path)
		}
	}
	result, err := ws.Search(SearchRequest{Query: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != len(formats) || !result.Coverage.Complete {
		t.Fatalf("document search = %+v", result)
	}
	for _, hit := range result.Hits {
		if _, err := ws.PreviewReplace(ws.Identity().ID, hit.Range, []byte("found!")); err != nil {
			t.Fatalf("preview %s: %v", hit.Path, err)
		}
		if _, _, err := ws.ApplyReplace(ws.Identity().ID, hit.Range, []byte("found!")); err != nil {
			t.Fatalf("edit %s: %v", hit.Path, err)
		}
	}
	if _, err := ws.Read(sibling); err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("sibling read error = %v", err)
	}
	inspection := ws.Inspect()
	if inspection.Workspace.Kind != KindDocuments || inspection.Coverage.Semantic != "text_only" ||
		inspection.Optional["provider"] != "unavailable" || !inspection.Native["guarded_edit"] {
		t.Fatalf("inspection does not disclose graceful degradation: %+v", inspection)
	}
}

func TestOpenDocumentOutsideProjectDoesNotRequireParentScan(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(home, ".huyang-s06-home-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	file := filepath.Join(root, "standalone.txt")
	sibling := filepath.Join(root, "do-not-index.txt")
	writeFile(t, file, "standalone")
	writeFile(t, sibling, "sibling")

	ws, err := OpenDocument(file, 0, filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	orientation, err := ws.Orient()
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{orientation.Entries[0].Path}; !reflect.DeepEqual(got, []string{"standalone.txt"}) {
		t.Fatalf("oriented paths = %v", got)
	}
	if _, err := ws.Read(sibling); err == nil {
		t.Fatal("open_document admitted a sibling")
	}
}

func TestNativeSearchLiteralRegexAndUnicodeCoordinates(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "unicode.txt")
	writeFile(t, path, "αβ needle\nneedle42 needle\n")
	ws := newNativeWorkspace(t, KindProject, root, nil, Limits{})

	literal, err := ws.Search(SearchRequest{Query: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if len(literal.Hits) != 3 || literal.Hits[0].Line != 1 || literal.Hits[0].Column != 4 {
		t.Fatalf("literal hits = %+v", literal.Hits)
	}
	regex, err := ws.Search(SearchRequest{Query: `needle[0-9]+`, Mode: SearchRegex})
	if err != nil {
		t.Fatal(err)
	}
	if len(regex.Hits) != 1 || regex.Hits[0].Match != "needle42" {
		t.Fatalf("regex hits = %+v", regex.Hits)
	}
	if _, err := ws.Search(SearchRequest{Query: "[", Mode: SearchRegex}); err == nil {
		t.Fatal("invalid regex was accepted")
	}
}

// A binary file costs the text search nothing and is not searched, and the
// reply says which file that was. The two facts belong together: skipping it
// is right, and a caller that cannot see the skip has no way to tell "no
// match in artifact.bin" from "artifact.bin was never opened".
func TestNativeSearchNamesTheBinaryFilesItDidNotSearch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "source.txt"), "needle\n")
	if err := os.WriteFile(filepath.Join(root, "artifact.bin"), []byte{0, 1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	ws := newNativeWorkspace(t, KindProject, root, nil, Limits{})
	result, err := ws.Search(SearchRequest{Query: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 || result.Coverage.BytesRead != int64(len("needle\n")) {
		t.Fatalf("binary file cost the text search: %+v", result)
	}
	if result.Coverage.Complete || result.Coverage.SkippedCount != 1 ||
		len(result.Coverage.Skipped) != 1 || !strings.Contains(result.Coverage.Skipped[0], "artifact.bin") {
		t.Fatalf("the skipped binary is invisible in coverage: %+v", result.Coverage)
	}
}

func TestExactByteReadPreviewAndApply(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bytes.txt")
	before := []byte("\xef\xbb\xbfalpha\r\nkeep \t\r\nomega")
	if err := os.WriteFile(path, before, 0o640); err != nil {
		t.Fatal(err)
	}
	ws := newNativeWorkspace(t, KindProject, root, nil, Limits{})
	read, err := ws.Read("bytes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read.Content, before) {
		t.Fatalf("read changed bytes: %q", read.Content)
	}
	start := strings.Index(string(before), "alpha")
	handle, err := ws.NewRange("bytes.txt", start, start+len("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	change, err := ws.PreviewReplace(ws.Identity().ID, handle, []byte("ALPHA"))
	if err != nil {
		t.Fatal(err)
	}
	if disk, err := os.ReadFile(path); err != nil || !reflect.DeepEqual(disk, before) {
		t.Fatalf("preview mutated disk: %q, %v", disk, err)
	}
	if change.Diff.BeforeSHA256 != hashBytes(before) || change.Diff.AfterSHA256 != change.AfterHash ||
		!strings.Contains(change.Diff.Patch, "@@ bytes") {
		t.Fatalf("diff lacks exact evidence: %+v", change.Diff)
	}
	_, after, err := ws.ApplyReplace(ws.Identity().ID, handle, []byte("ALPHA"))
	if err != nil {
		t.Fatal(err)
	}
	want := bytesReplace(before, start, start+len("alpha"), []byte("ALPHA"))
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(disk, want) || after.ContentSHA256 != hashBytes(want) {
		t.Fatalf("apply bytes = %q, snapshot = %+v", disk, after)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode changed to %o", info.Mode().Perm())
	}
}

func bytesReplace(content []byte, start, end int, replacement []byte) []byte {
	result := append([]byte(nil), content[:start]...)
	result = append(result, replacement...)
	return append(result, content[end:]...)
}

func TestRangeGuardRejectsStaleRevisionAndTamperedAnchors(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "guard.txt")
	writeFile(t, path, "before target after")
	ws := newNativeWorkspace(t, KindProject, root, nil, Limits{})
	handle, err := ws.NewRange("guard.txt", 7, 13)
	if err != nil {
		t.Fatal(err)
	}
	tampered := handle
	tampered.BeforeSHA256 = hashBytes([]byte("wrong"))
	if _, err := ws.PreviewReplace(ws.Identity().ID, tampered, []byte("new")); conflictCode(t, err) != ConflictDocumentChanged {
		t.Fatalf("tampered anchor error = %v", err)
	}
	writeFile(t, path, "before changed after")
	if _, err := ws.PreviewReplace(ws.Identity().ID, handle, []byte("new")); conflictCode(t, err) != ConflictDocumentChanged {
		t.Fatalf("stale revision error = %v", err)
	}
}

func TestNativeCreateReplaceDeleteUseRevisionPreconditions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	ws := newNativeWorkspace(t, KindProject, root, nil, Limits{})
	missing, err := ws.Snapshot(path, ProviderLayer{})
	if err != nil {
		t.Fatal(err)
	}
	_, created, err := ws.ApplyFile(ws.Identity().ID, path, missing.Revision, FileCreate, []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Disk.Kind != ObjectRegularText {
		t.Fatalf("created kind = %s", created.Disk.Kind)
	}
	_, replaced, err := ws.ApplyFile(ws.Identity().ID, path, created.Revision, FileReplace, []byte("two"))
	if err != nil {
		t.Fatal(err)
	}
	diff, deleted, err := ws.ApplyFile(ws.Identity().ID, path, replaced.Revision, FileDelete, nil)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Disk.Kind != ObjectMissing || string(diff.Before) != "two" || len(diff.After) != 0 {
		t.Fatalf("delete result = %+v %+v", diff, deleted)
	}
	if _, _, err := ws.ApplyFile(ws.Identity().ID, path, created.Revision, FileReplace, []byte("stale")); err == nil {
		t.Fatal("stale file revision was accepted")
	}
}

func TestBoundedWalkerSkipsGitAndDisclosesCaps(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", ".git/secret", "deep/one/two/three.txt"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, "text")
	}
	ws := newNativeWorkspace(t, KindProject, root, nil, Limits{MaxFiles: 1, MaxDepth: 2, MaxBytes: 1024})
	orientation, err := ws.Orient()
	if err != nil {
		t.Fatal(err)
	}
	if orientation.Coverage.Complete || !orientation.Coverage.Capped || len(orientation.Entries) != 1 {
		t.Fatalf("bounded orientation = %+v", orientation)
	}
	for _, entry := range orientation.Entries {
		if strings.Contains(entry.Path, ".git") {
			t.Fatalf("walker included Git internals: %+v", orientation)
		}
	}
}

func TestSearchDoesNotSpendTextBudgetOnBinaryFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a-binary"), bytes.Repeat([]byte{0}, 900), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "z-source.go"), "package fixture\nfunc needle() {}\n")
	ws := newNativeWorkspace(t, KindProject, root, nil, Limits{MaxFiles: 20, MaxDepth: 4, MaxBytes: 128, MaxMatches: 20})
	result, err := ws.Search(SearchRequest{Query: "needle", Mode: SearchLiteral})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 || result.Hits[0].Path != "z-source.go" {
		t.Fatalf("binary consumed searchable text budget: %+v", result)
	}
	// Not spending the budget on it is right; saying nothing about it is not.
	// "No matches in a-binary" and "a-binary was never opened" are different
	// answers, and only coverage can tell them apart.
	if result.Coverage.Complete || result.Coverage.SkippedCount != 1 {
		t.Fatalf("a search that skipped a binary reported complete coverage: %+v", result.Coverage)
	}
	if len(result.Coverage.Skipped) != 1 || !strings.Contains(result.Coverage.Skipped[0], "a-binary") {
		t.Fatalf("coverage does not name the skipped binary: %+v", result.Coverage)
	}
}

// The skipped list is a bounded sample and the count next to it is exact, so
// a tree full of binaries cannot turn one coverage block into a listing of
// the tree.
func TestCoverageSkippedListIsBounded(t *testing.T) {
	root := t.TempDir()
	binaries := maxCoverageSkipped + 5
	for index := range binaries {
		name := filepath.Join(root, fmt.Sprintf("blob-%02d.bin", index))
		if err := os.WriteFile(name, bytes.Repeat([]byte{0}, 64), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "source.go"), "package fixture\nfunc needle() {}\n")
	ws := newNativeWorkspace(t, KindProject, root, nil, Limits{MaxFiles: 100, MaxDepth: 4, MaxBytes: 1 << 20, MaxMatches: 20})
	result, err := ws.Search(SearchRequest{Query: "needle", Mode: SearchLiteral})
	if err != nil {
		t.Fatal(err)
	}
	if result.Coverage.SkippedCount != binaries || len(result.Coverage.Skipped) != maxCoverageSkipped {
		t.Fatalf("skipped list is not a bounded sample of an exact count: %+v", result.Coverage)
	}
}

func TestOptionalSectionerAndTextFallback(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.md")
	writeFile(t, path, "# title\nbody\n")

	textOnly := newNativeWorkspace(t, KindDocuments, root, []string{path}, Limits{})
	fallback, err := textOnly.Outline(path)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Coverage.Semantic != "text_only" || fallback.Fallback == nil || len(fallback.Sections) != 0 {
		t.Fatalf("fallback outline = %+v", fallback)
	}

	parsed, err := Open(OpenOptions{
		Kind: KindDocuments, Root: root, Files: []string{path}, StateDir: filepath.Join(t.TempDir(), "state"),
		Sectioner: fixtureSectioner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	outline, err := parsed.Outline(path)
	if err != nil {
		t.Fatal(err)
	}
	if outline.Coverage.Semantic != "parser_sections" || len(outline.Sections) != 1 || outline.Fallback != nil {
		t.Fatalf("parser outline = %+v", outline)
	}
}

func TestInspectionSanitizesEnvironmentFailures(t *testing.T) {
	root := t.TempDir()
	ws := newNativeWorkspace(t, KindProject, root, nil, Limits{})
	ws.RecordEnvironmentFailure(EnvironmentFailure{
		Layer: "provider\x00", Code: "provider_launch_failed", Executable: "/usr/bin/nvim",
		Arguments: []string{"--headless", "token=top-secret"}, WorkingDirectory: root,
		Stderr: "bad\x00message",
	})
	failure := ws.Inspect().Failures[0]
	if failure.Layer != "provider" || failure.Arguments[1] != "<redacted>" || failure.Stderr != "badmessage" {
		t.Fatalf("unsanitized failure = %+v", failure)
	}
}

func TestRecoveryRestoresPostimageAndPreservesThirdPartyWrite(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "recover.txt")
	writeFile(t, path, "after")
	stateDir := filepath.Join(t.TempDir(), "state")
	ws := newNativeWorkspace(t, KindProject, root, nil, Limits{})
	ws.stateDir = stateDir
	record := journalRecord{
		Version: 1, Path: path, Mode: uint32(0o644), PreExists: true, PostExists: true,
		Preimage:  base64.StdEncoding.EncodeToString([]byte("before")),
		Postimage: base64.StdEncoding.EncodeToString([]byte("after")),
	}
	writeJournalFixture(t, stateDir, "native-recover.json", record)
	result, err := ws.Recover()
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "before" || len(result.Recovered) != 1 {
		t.Fatalf("recovery = %+v, content = %q", result, content)
	}

	writeFile(t, path, "third-party")
	writeJournalFixture(t, stateDir, "native-conflict.json", record)
	result, err = ws.Recover()
	if err != nil {
		t.Fatal(err)
	}
	content, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "third-party" || len(result.Conflicts) != 1 {
		t.Fatalf("conflict recovery overwrote external write: %+v %q", result, content)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "native-conflict.json")); err != nil {
		t.Fatalf("conflicted journal was removed: %v", err)
	}
}

func TestProjectDiscoveryAndImpactSkipNodeModules(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{filepath.Join(root, "src"), filepath.Join(root, "node_modules", "pkg")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "src", "app.ts"), "export const app = 1\n")
	for _, name := range []string{"dep-a.js", "dep-b.js", "dep-c.js"} {
		writeFile(t, filepath.Join(root, "node_modules", "pkg", name), "needle\n")
	}
	ws := newNativeWorkspace(t, KindProject, root, nil, Limits{MaxFiles: 2})
	orientation, err := ws.Orient()
	if err != nil {
		t.Fatal(err)
	}
	if !orientation.Coverage.Complete || len(orientation.Entries) != 1 || orientation.Entries[0].Path != "src/app.ts" {
		t.Fatalf("node_modules consumed workspace limits: %+v", orientation)
	}
	search, err := ws.Search(SearchRequest{Query: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if !search.Coverage.Complete || len(search.Hits) != 0 {
		t.Fatalf("node_modules leaked into search: %+v", search)
	}
	graph, err := BuildImpactGraph(root, "rev_node_modules", []string{"src/app.ts"}, ImpactPolicy{MaxFiles: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !graph.Coverage.Complete || len(graph.Nodes) != 1 || graph.Nodes[0].Path != "src/app.ts" {
		t.Fatalf("node_modules consumed impact limits: %+v", graph)
	}
}

func writeJournalFixture(t *testing.T, stateDir, name string, record journalRecord) {
	t.Helper()
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, name), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

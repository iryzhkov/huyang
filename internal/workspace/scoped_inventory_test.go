package workspace

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestScopedSearchEnumerationBudget(t *testing.T) {
	for _, git := range []bool{false, true} {
		t.Run(fmt.Sprint("git=", git), func(t *testing.T) {
			root := t.TempDir()
			for i := range 6 {
				writeFile(t, filepath.Join(root, fmt.Sprintf("aaa%d.txt", i)), "noise\n")
			}
			writeFile(t, filepath.Join(root, "zzz.txt"), strings.Repeat("padding\n", 15000)+"needle\n")
			if git {
				if output, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
					t.Fatalf("git: %v %s", err, output)
				}
			}
			ws := newNativeWorkspace(t, KindProject, root, nil, Limits{MaxFiles: 2})
			broad, err := ws.Search(SearchRequest{Query: "needle"})
			if err != nil || broad.Coverage.Complete || !broad.Coverage.Capped {
				t.Fatalf("broad: %+v %v", broad, err)
			}
			for _, query := range []string{"needle", "absent"} {
				result, err := ws.Search(SearchRequest{Query: query, Paths: []string{"zzz.txt"}})
				if err != nil || !result.Coverage.Complete || result.Coverage.Capped || result.Coverage.FilesRead != 1 {
					t.Fatalf("scoped %s: %+v %v", query, result, err)
				}
				if query == "needle" && len(result.Hits) != 1 {
					t.Fatalf("lost scoped match: %+v", result)
				}
				if query == "absent" && len(result.Hits) != 0 {
					t.Fatalf("unexpected match: %+v", result)
				}
				if _, err := ws.InspectResultSet(result.ResultSet.Handle); err != nil {
					t.Fatalf("scope cannot be revalidated: %v", err)
				}
			}
		})
	}
}

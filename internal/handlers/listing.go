package handlers

import (
	"fmt"
	"strings"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// maxDirectoryEntries bounds the entries a directory read lists. A listing
// is for orientation; a directory with more children than this is narrowed
// with search paths or by reading one of the subdirectories it names.
const maxDirectoryEntries = 200

// listedEntry is one line of a listing: a file, or a directory with the
// number of listed files below it.
type listedEntry struct {
	Path      string
	Directory bool
	Files     int
}

// pathListing is a bounded listing of what a workspace holds.
type pathListing struct {
	Entries []listedEntry
	// Total counts every entry before the bound, so a cut listing says so.
	Total    int
	Coverage workspacecore.Coverage
}

func (l pathListing) truncated() bool { return l.Total > len(l.Entries) }

// listWorkspacePaths is the one listing a directory read and an empty-query
// search share. It lists the documents search would consider - the Git
// inventory, or the bounded walk where Git cannot list the tree - so it
// honours the same .gitignore, depth and file-cap rules, and nothing past a
// symlink that leaves the root is listed, because neither the inventory nor
// the walk follows one. With a directory it lists that directory's
// immediate children, counting the files below each subdirectory; "" is the
// workspace root. Without one it lists every file inside patterns. At most
// limit entries are kept.
func listWorkspacePaths(workspace *workspacecore.Workspace, directory string, patterns []string, limit int) (pathListing, error) {
	grouped := directory != "" || len(patterns) == 0
	prefix := ""
	if directory != "" {
		prefix = strings.TrimSuffix(directory, "/") + "/"
		patterns = []string{prefix}
	}
	paths, coverage, err := workspace.ScopedPaths(patterns)
	if err != nil {
		return pathListing{}, err
	}
	listing := pathListing{Coverage: coverage}
	index := map[string]int{}
	for _, path := range paths {
		entry := listedEntry{Path: path}
		if grouped {
			// The scope is a substring match, so a/b/ also matches x/a/b/;
			// only the directory itself is listed.
			rest, inside := strings.CutPrefix(path, prefix)
			if !inside || rest == "" {
				continue
			}
			if child, _, nested := strings.Cut(rest, "/"); nested {
				entry = listedEntry{Path: prefix + child, Directory: true}
			}
		}
		if position, seen := index[entry.Path]; seen {
			if position >= 0 {
				listing.Entries[position].Files++
			}
			continue
		}
		listing.Total++
		if entry.Directory {
			entry.Files = 1
		}
		if len(listing.Entries) >= limit {
			index[entry.Path] = -1
			continue
		}
		index[entry.Path] = len(listing.Entries)
		listing.Entries = append(listing.Entries, entry)
	}
	return listing, nil
}

// directoryListingData is the reply body of a directory read: the entries
// in the shape workspace_open's overview uses, the counts, and the
// coverage when the listing could not see the whole directory.
func directoryListingData(directory string, listing pathListing) (map[string]any, string) {
	entries := make([]map[string]any, 0, len(listing.Entries))
	files, directories := 0, 0
	for _, entry := range listing.Entries {
		if entry.Directory {
			directories++
			entries = append(entries, map[string]any{"path": entry.Path, "kind": "directory", "files": entry.Files})
			continue
		}
		files++
		entries = append(entries, map[string]any{"path": entry.Path, "kind": "file"})
	}
	label := directory
	if label == "" {
		label = "."
	}
	data := map[string]any{
		"path": label, "kind": "directory", "entries": entries,
		"entry_count": listing.Total, "listed_count": len(entries),
	}
	if !listing.Coverage.Complete {
		data["coverage"] = listing.Coverage
	}
	summary := fmt.Sprintf("Listed directory %s: %d files and %d directories", label, files, directories)
	switch {
	case listing.Total == 0:
		summary = fmt.Sprintf("Directory %s holds no file this workspace lists; it is empty or every file in it is ignored", label)
	case listing.truncated():
		data["truncated"] = true
		summary = fmt.Sprintf("Listed directory %s: the first %d of %d entries; truncated at the listing bound", label, len(entries), listing.Total)
	}
	return data, summary
}

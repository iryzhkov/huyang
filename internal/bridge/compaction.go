package bridge

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// Result compaction. The contract is compact by default with detail behind
// IDs and explicit arguments: search hits carry anchors only on request,
// diagnostics list bodies only when evidence exists, the workspace overview
// summarises directories, and diagnostic notices are delivered once per
// client.

const (
	maxDiagnosticUpdates = 20
	maxNoticeClients     = 64
)

// clientIdentityKey carries the MCP session identity through the request
// context so per-client delivery state can be keyed without a session object.
type clientIdentityKey struct{}

func withClientIdentity(ctx context.Context, identity string) context.Context {
	return context.WithValue(ctx, clientIdentityKey{}, identity)
}

func clientIdentity(ctx context.Context) string {
	identity, _ := ctx.Value(clientIdentityKey{}).(string)
	return identity
}

// noticeDelivery remembers, per workspace and client, the last diagnostic
// notice cursor already delivered, so diagnostic_updates is a delta rather
// than the same pending notices on every reply.
type noticeDelivery struct {
	mu        sync.Mutex
	delivered map[workspacecore.ID]*clientCursors
}

type clientCursors struct {
	cursors map[string]uint64
	order   []string
}

func newNoticeDelivery() *noticeDelivery {
	return &noticeDelivery{delivered: make(map[workspacecore.ID]*clientCursors)}
}

func (n *noticeDelivery) last(workspaceID workspacecore.ID, client string) uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	if clients := n.delivered[workspaceID]; clients != nil {
		return clients.cursors[client]
	}
	return 0
}

func (n *noticeDelivery) record(workspaceID workspacecore.ID, client string, cursor uint64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	clients := n.delivered[workspaceID]
	if clients == nil {
		clients = &clientCursors{cursors: make(map[string]uint64)}
		n.delivered[workspaceID] = clients
	}
	if _, known := clients.cursors[client]; !known {
		clients.order = append(clients.order, client)
		for len(clients.order) > maxNoticeClients {
			delete(clients.cursors, clients.order[0])
			clients.order = clients.order[1:]
		}
	}
	if cursor > clients.cursors[client] {
		clients.cursors[client] = cursor
	}
}

func diagnosticCursorSequence(cursor string) uint64 {
	var sequence uint64
	if _, err := fmt.Sscanf(cursor, "diagcur_%d", &sequence); err != nil {
		return 0
	}
	return sequence
}

// attachDiagnosticUpdates adds the diagnostic notices this client has not yet
// seen, capped at maxDiagnosticUpdates with a truncation marker, and omits the
// field entirely when nothing is new. The delta is computed from the
// workspace's notices-after-cursor query against the last cursor delivered to
// this client; acknowledgement through the diagnostics cursor prunes notices
// for every client.
func (d *directWorkspaces) attachDiagnosticUpdates(ctx context.Context, workspace *workspacecore.Workspace, result map[string]any) {
	workspaceID := workspace.Identity().ID
	client := clientIdentity(ctx)
	last := d.notices.last(workspaceID, client)
	page, err := workspace.DiagnosticNoticesSince(fmt.Sprintf("diagcur_%d", last), maxDiagnosticUpdates)
	if err != nil || len(page.Notices) == 0 {
		return
	}
	result["diagnostic_updates"] = page.Notices
	if page.More || page.Truncated {
		result["diagnostic_updates_truncated"] = true
	}
	d.notices.record(workspaceID, client, diagnosticCursorSequence(page.Cursor))
}

// compactDiagnosticReport shapes the diagnostics tool result. Evidence IDs
// are reported once, on the envelope, so the report itself carries none;
// resolved findings collapse to their IDs; when no evidence exists only the
// reasons and counts are returned. full restores the raw report.
func compactDiagnosticReport(report workspacecore.DiagnosticReport, outcome string, full bool) map[string]any {
	if full {
		return map[string]any{"diagnostics": report}
	}
	coverage := make(map[string]any, len(report.Coverage))
	for name, dimension := range report.Coverage {
		coverage[name] = map[string]any{
			"state": dimension.State, "confidence": dimension.Confidence,
			"reasons": nonNilStrings(dimension.Reasons),
		}
	}
	resolvedIDs := make([]string, 0, len(report.Resolved))
	for _, item := range report.Resolved {
		resolvedIDs = append(resolvedIDs, item.ID)
	}
	compact := map[string]any{
		"confidence": report.Confidence, "coverage": coverage, "cursor": report.Cursor,
		"new_count": len(report.New), "resolved_count": len(report.Resolved), "resolved_ids": resolvedIDs,
		"preexisting_count": report.PreexistingCount, "provisional_reasons": nonNilStrings(report.ProvisionalReasons),
	}
	if outcome != "unavailable" {
		items := make([]map[string]any, 0, len(report.New))
		for _, item := range report.New {
			items = append(items, compactDiagnosticItem(item))
		}
		compact["new"] = items
	}
	return map[string]any{"diagnostics": compact}
}

func compactDiagnosticItem(item workspacecore.DiagnosticItem) map[string]any {
	compact := map[string]any{
		"id": item.ID, "document": item.Document, "producer": item.Producer,
		"severity": item.Finding.Severity, "range": item.Finding.Range, "message": item.Finding.Message,
		"attribution": item.Attribution.Rank,
	}
	if item.Finding.Code != "" {
		compact["code"] = item.Finding.Code
	}
	if item.Finding.Source != "" {
		compact["source"] = item.Finding.Source
	}
	if item.DocumentRevision != "" {
		compact["document_revision"] = item.DocumentRevision
	}
	if item.TransactionID != "" {
		compact["transaction_id"] = item.TransactionID
	}
	return compact
}

// compactSearchHits returns the bounded hit list. The default hit carries
// path, line, column, match and the editable handle; the exact byte anchors
// are added only with include_ranges.
func compactSearchHits(hits []workspacecore.SearchHit, limit int, includeRanges bool) ([]map[string]any, bool) {
	if limit <= 0 {
		limit = 100
	}
	returned := hits
	if len(returned) > limit {
		returned = returned[:limit]
	}
	compact := make([]map[string]any, 0, len(returned))
	for _, hit := range returned {
		item := map[string]any{
			"path": hit.Path, "line": hit.Line, "column": hit.Column, "match": hit.Match,
		}
		if hit.MatchHandle != nil {
			item["handle"] = hit.MatchHandle.Handle
		}
		if includeRanges {
			item["byte_start"], item["byte_end"], item["range"] = hit.ByteStart, hit.ByteEnd, hit.Range
		}
		compact = append(compact, item)
	}
	return compact, len(hits) > len(returned)
}

// compactOrientation summarises a workspace overview by top-level entry:
// directories carry their file count and byte total, files their kind and
// size. The full entry listing stays behind overview=full.
func compactOrientation(orientation workspacecore.Orientation) map[string]any {
	type summary struct {
		path  string
		kind  string
		files int
		bytes int64
	}
	byName := map[string]*summary{}
	var order []string
	for _, entry := range orientation.Entries {
		name := entry.Path
		kind := string(entry.Kind)
		if slash := strings.IndexByte(entry.Path, '/'); slash >= 0 {
			name = entry.Path[:slash]
			kind = "directory"
		}
		item := byName[name]
		if item == nil {
			item = &summary{path: name, kind: kind}
			byName[name] = item
			order = append(order, name)
		}
		item.files++
		item.bytes += entry.Size
	}
	sort.Strings(order)
	truncated := len(order) > maxStructuredEntries
	if truncated {
		order = order[:maxStructuredEntries]
	}
	topLevel := make([]map[string]any, 0, len(order))
	for _, name := range order {
		item := byName[name]
		record := map[string]any{"path": item.path, "kind": item.kind, "bytes": item.bytes}
		if item.kind == "directory" {
			record["files"] = item.files
		}
		topLevel = append(topLevel, record)
	}
	return map[string]any{
		"workspace": orientation.Workspace, "coverage": orientation.Coverage,
		"entry_count": len(orientation.Entries), "top_level": topLevel,
		"top_level_count": len(byName), "top_level_truncated": truncated,
	}
}

// compactRecentCommits keeps the handle, abbreviated ID and subject of each
// commit; the full summary is available through read view=changes.
func compactRecentCommits(list workspacecore.CommitList, limit int) map[string]any {
	commits := make([]map[string]any, 0, min(len(list.Commits), limit))
	for _, commit := range list.Commits[:min(len(list.Commits), limit)] {
		commits = append(commits, map[string]any{
			"handle": commit.Handle, "abbreviated_id": commit.AbbreviatedID, "subject": commit.Subject,
		})
	}
	return map[string]any{"commits": commits, "coverage": list.Coverage}
}

// compactLanguageSupport removes the per-language install option lists from
// the provider's support report and replaces each with the key under which
// the same options appear once in install_options.
func compactLanguageSupport(support map[string]any) map[string]any {
	compact := cloneEnvelope(support)
	languages := anySlice(support["languages"])
	entries := make([]any, 0, len(languages))
	for _, raw := range languages {
		entry, ok := raw.(map[string]any)
		if !ok {
			entries = append(entries, raw)
			continue
		}
		copied := cloneEnvelope(entry)
		if options := anySlice(entry["install_options"]); len(options) > 0 {
			copied["install_options_key"] = fmt.Sprint(entry["filetype"])
		}
		delete(copied, "install_options")
		entries = append(entries, copied)
	}
	compact["languages"] = entries
	return compact
}

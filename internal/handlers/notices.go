package handlers

import (
	"context"
	"fmt"
	"strings"
	"sync"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

const (
	// MaxDiagnosticUpdates bounds the collapsed notices one reply carries;
	// the full backlog stays readable through the diagnostics tool.
	MaxDiagnosticUpdates = 5
	maxNoticeClients     = 64
	// noticeScanLimit is how far past the client's cursor one reply looks,
	// so the delivered cursor reaches the newest notice instead of replaying
	// an old backlog a page at a time.
	noticeScanLimit = 500
)

// DiagnosticUpdate is one collapsed notice: the last state of a finding
// since the client's previous reply, as a workspace-relative path, without
// the cursor or an empty attribution.
type DiagnosticUpdate struct {
	ID          string                            `json:"id"`
	Kind        string                            `json:"kind"`
	Severity    int                               `json:"severity,omitempty"`
	Path        string                            `json:"path,omitempty"`
	Attribution *workspacecore.CulpritAttribution `json:"attribution,omitempty"`
}

// clientIdentityKey carries the MCP session identity through the request
// context so per-client delivery state can be keyed without a session object.
type clientIdentityKey struct{}

func WithClientIdentity(ctx context.Context, identity string) context.Context {
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

// last is the newest notice already delivered to this client, and whether
// the client has been seen at all.
func (n *noticeDelivery) last(workspaceID workspacecore.ID, client string) (uint64, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if clients := n.delivered[workspaceID]; clients != nil {
		cursor, known := clients.cursors[client]
		return cursor, known
	}
	return 0, false
}

func (n *noticeDelivery) record(workspaceID workspacecore.ID, client string, cursor uint64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	clients := n.track(workspaceID, client)
	if cursor > clients.cursors[client] {
		clients.cursors[client] = cursor
	}
}

// Register starts a client at head, the newest notice recorded before its
// first call ran. A client that has just connected is not owed the backlog
// of everything that happened before it existed, and starting it here rather
// than after the call keeps the findings its own call produced. A client
// already known keeps its cursor. The result reports that this client had
// not met this workspace before, which is also when it is told the rules.
func (n *noticeDelivery) Register(workspaceID workspacecore.ID, client string, head uint64) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if clients := n.delivered[workspaceID]; clients != nil {
		if _, known := clients.cursors[client]; known {
			return false
		}
	}
	n.track(workspaceID, client).cursors[client] = head
	return true
}

// track returns the cursor table of a workspace, adding the client to it and
// evicting the oldest when the table is full. The caller holds the lock.
func (n *noticeDelivery) track(workspaceID workspacecore.ID, client string) *clientCursors {
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
	return clients
}

func diagnosticCursorSequence(cursor string) uint64 {
	var sequence uint64
	if _, err := fmt.Sscanf(cursor, "diagcur_%d", &sequence); err != nil {
		return 0
	}
	return sequence
}

// AttachDiagnosticUpdates adds the diagnostic notices this client has not
// yet seen, collapsed to the last state of each finding and capped at
// MaxDiagnosticUpdates newest entries with a truncation marker; the field is
// omitted when nothing is new. A client is registered at the current head on
// its first reply and sees deltas from there. The client's cursor always
// advances to the newest notice examined, so a busy repository never replays
// its backlog a page at a time. Acknowledgement through the diagnostics cursor prunes
// notices for every client.
func (h *Handlers) AttachDiagnosticUpdates(ctx context.Context, workspace *workspacecore.Workspace, result map[string]any) {
	workspaceID := workspace.Identity().ID
	client := clientIdentity(ctx)
	last, known := h.notices.last(workspaceID, client)
	if !known {
		// A client that has just connected has no backlog to catch up on.
		// The delta says what this session changed; findings that were
		// already there, possibly from another session or before a service
		// restart, are what the diagnostics tool is for. Delivering them as
		// new was read as an edit having caused them.
		h.notices.record(workspaceID, client, workspace.DiagnosticNoticeHead())
		return
	}
	page, err := workspace.DiagnosticNoticesSince(fmt.Sprintf("diagcur_%d", last), noticeScanLimit)
	if err != nil || len(page.Notices) == 0 {
		return
	}
	h.notices.record(workspaceID, client, diagnosticCursorSequence(page.Cursor))
	updates, dropped := collapseNotices(workspace.Identity().Root, page.Notices)
	if len(updates) == 0 {
		return
	}
	result["diagnostic_updates"] = updates
	if dropped || page.More || page.Truncated {
		result["diagnostic_updates_truncated"] = true
	}
}

// collapseNotices keeps one entry per finding, newest first, its last kind
// winning. A finding whose last word is stale is dropped: its content moved
// and the next refresh reports it again as new or resolved. The second
// result reports that more findings changed than the cap allows.
func collapseNotices(root string, notices []workspacecore.DiagnosticNotice) ([]DiagnosticUpdate, bool) {
	seen := make(map[string]bool, len(notices))
	updates := make([]DiagnosticUpdate, 0, MaxDiagnosticUpdates)
	for index := len(notices) - 1; index >= 0; index-- {
		notice := notices[index]
		if seen[notice.ID] {
			continue
		}
		seen[notice.ID] = true
		if notice.Kind == "stale" {
			continue
		}
		if len(updates) == MaxDiagnosticUpdates {
			return updates, true
		}
		update := DiagnosticUpdate{ID: notice.ID, Kind: notice.Kind, Severity: notice.Severity, Path: strings.TrimPrefix(notice.Document, root+"/")}
		if notice.Attribution.Rank != "" && notice.Attribution.Rank != "unattributed" {
			attribution := notice.Attribution
			update.Attribution = &attribution
		}
		updates = append(updates, update)
	}
	return updates, false
}

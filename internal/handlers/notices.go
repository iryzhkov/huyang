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
	Line        int                               `json:"line,omitempty"`
	Message     string                            `json:"message,omitempty"`
	Attribution *workspacecore.CulpritAttribution `json:"attribution,omitempty"`
}

// maxNoticeMessage bounds the message a notice carries. A compiler says what
// is wrong in a line; a type checker can say it in a paragraph, and the
// paragraph belongs in diagnostics, not in the delta of every reply.
const maxNoticeMessage = 160

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
	// owed are the clients that have not been told the rules yet. The
	// reply that registers a client is not always one the client sees:
	// the service resolves a root into a workspace before the call runs
	// and discards that reply, so the debt is remembered here and settled
	// by the first reply that does reach the client.
	owed map[string]bool
	// unavailable is the last diagnostic unavailability each client was
	// told about. A workspace with no language server repeats the same
	// reason and the same recovery on every edit of a session, which is a
	// paragraph the client has already read and cannot act on twice.
	unavailable map[string]string
	order       []string
}

func newNoticeDelivery() *noticeDelivery {
	return &noticeDelivery{delivered: make(map[workspacecore.ID]*clientCursors)}
}

// TakeGuide reports whether this client still has to be told the rules for
// this workspace, and records that it now has been.
func (n *noticeDelivery) TakeGuide(workspaceID workspacecore.ID, client string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	clients := n.delivered[workspaceID]
	if clients == nil || !clients.owed[client] {
		return false
	}
	delete(clients.owed, client)
	return true
}

// TakeUnavailability reports whether this client has already been told this
// exact diagnostic unavailability for this workspace, and records it. An
// empty fingerprint clears the memory, so the next unavailability is told in
// full however often the verdict flips.
func (n *noticeDelivery) TakeUnavailability(workspaceID workspacecore.ID, client, fingerprint string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	clients := n.delivered[workspaceID]
	if clients == nil {
		return false
	}
	if clients.unavailable == nil {
		clients.unavailable = map[string]string{}
	}
	if fingerprint == "" {
		delete(clients.unavailable, client)
		return false
	}
	repeated := clients.unavailable[client] == fingerprint
	clients.unavailable[client] = fingerprint
	return repeated
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
// already known keeps its cursor. A client meeting this workspace for the
// first time is also owed the rules, which TakeGuide hands over.
func (n *noticeDelivery) Register(workspaceID workspacecore.ID, client string, head uint64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if clients := n.delivered[workspaceID]; clients != nil {
		if _, known := clients.cursors[client]; known {
			return
		}
	}
	clients := n.track(workspaceID, client)
	clients.cursors[client] = head
	if clients.owed == nil {
		clients.owed = map[string]bool{}
	}
	clients.owed[client] = true
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
			delete(clients.owed, clients.order[0])
			delete(clients.unavailable, clients.order[0])
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
	describeNotices(workspace, updates)
	result["diagnostic_updates"] = updates
	// The summary was written before the language server published these,
	// so an edit that says "no new diagnostics" can arrive with findings
	// beside it. Say what the reply carries rather than contradicting it.
	if summary, ok := result["summary"].(string); ok && strings.HasSuffix(summary, "no new diagnostics") {
		if count := countNewNotices(updates); count > 0 {
			result["summary"] = strings.TrimSuffix(summary, "no new diagnostics") +
				fmt.Sprintf("%d diagnostic(s) published since, in diagnostic_updates", count)
		}
	}
	if dropped || page.More || page.Truncated {
		result["diagnostic_updates_truncated"] = true
	}
}

// describeNotices fills in the line and the message of every update whose
// finding the ledger still holds.
func describeNotices(workspace *workspacecore.Workspace, updates []DiagnosticUpdate) {
	ids := make([]string, 0, len(updates))
	for _, update := range updates {
		if update.Kind != "resolved" {
			ids = append(ids, update.ID)
		}
	}
	findings := workspace.DiagnosticFindings(ids)
	for index, update := range updates {
		finding, known := findings[update.ID]
		if !known {
			continue
		}
		updates[index].Line = finding.Range.StartLine
		message := finding.Message
		if len(message) > maxNoticeMessage {
			message = message[:maxNoticeMessage] + "..."
		}
		updates[index].Message = message
	}
}

// countNewNotices counts the findings a delta reports as new.
func countNewNotices(updates []DiagnosticUpdate) int {
	count := 0
	for _, update := range updates {
		if update.Kind == "new" {
			count++
		}
	}
	return count
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

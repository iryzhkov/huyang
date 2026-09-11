package bridge

import (
	"context"
	"fmt"
	"sync"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

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
func (h *toolHandlers) attachDiagnosticUpdates(ctx context.Context, workspace *workspacecore.Workspace, result map[string]any) {
	workspaceID := workspace.Identity().ID
	client := clientIdentity(ctx)
	last := h.notices.last(workspaceID, client)
	page, err := workspace.DiagnosticNoticesSince(fmt.Sprintf("diagcur_%d", last), maxDiagnosticUpdates)
	if err != nil || len(page.Notices) == 0 {
		return
	}
	result["diagnostic_updates"] = page.Notices
	if page.More || page.Truncated {
		result["diagnostic_updates_truncated"] = true
	}
	h.notices.record(workspaceID, client, diagnosticCursorSequence(page.Cursor))
}

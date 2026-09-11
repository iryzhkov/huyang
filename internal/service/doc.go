// Package service is the long-lived Huyang process: the Unix control socket
// and optional loopback HTTP listeners, the stdio proxy behind huyang mcp,
// the MCP SDK registration of the mcpapi catalog, and the dispatcher that
// runs every tool call through the workspace registry, the idempotency
// receipts and the per-workspace scheduler before handing it to the
// handlers. Nothing imports this package except cmd/huyang.
package service

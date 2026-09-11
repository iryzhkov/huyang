// Package mcpapi is the MCP-facing contract of the Huyang service: the tool
// catalog with its profiles, input schemas and scheduler classes, the result
// envelope and its output schema, argument validation, and the compaction
// rules that keep results bounded. It has no runtime state; the service
// registers the catalog and the handlers produce envelopes with it.
package mcpapi

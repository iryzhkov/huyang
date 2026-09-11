// Package bridge is empty. The service it held moved to internal/service,
// internal/handlers, internal/mcpapi and internal/providerpool. The package
// is retained only because tests/smoke.sh still names ./internal/bridge in
// its Go suite; delete it once the smoke script targets the four packages.
package bridge

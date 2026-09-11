// Package handlers implements every Huyang tool over the workspace core and
// the provider pool: one file per tool family, each turning validated
// arguments into workspace or provider calls and rendering an mcpapi
// envelope. The handlers hold no replay or scheduling state. They see the
// service's registry and receipts only through the WorkspaceLookup and
// RevisionProvenance interfaces, so they can be exercised with in-memory
// doubles; the service decides when a handler runs, the handlers decide
// what it does.
package handlers

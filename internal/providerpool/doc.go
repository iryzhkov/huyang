// Package providerpool owns every semantic provider the Huyang service
// spawns: the canonical or debug provider of each workspace, the sandbox
// stagers that prepare plans in isolation, the single request-context
// assembly for provider calls, and the recording of provider diagnostics as
// workspace evidence. It depends on the provider contract and the workspace
// core only; the handlers ask it for providers and stagers, and the service
// closes it on shutdown.
package providerpool

// Package provider defines the transport-independent boundary between the
// Huyang workspace core and semantic editor processes.
package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ID is a stable provider identity. Backends derive it from stable
// configuration, never process IDs or ephemeral endpoints.
type ID string

// Capability is a semantic operation family used for profile routing.
type Capability string

const (
	CapabilityExecute     Capability = "execute"
	CapabilityNavigation  Capability = "navigation"
	CapabilityRename      Capability = "rename"
	CapabilityDiagnostics Capability = "diagnostics"
	CapabilityCodeActions Capability = "code_actions"
)

// Role describes how a provider participates in one analysis profile.
type Role string

const (
	RolePrimary       Role = "primary"
	RoleComplementary Role = "complementary"
	RoleFallback      Role = "fallback"
)

// CancellationSupport states whether a backend can stop provider work after it
// has begun. Unsupported backends still receive cancellation and deadlines so
// callers can report the limitation accurately.
type CancellationSupport string

const (
	CancellationUnsupported     CancellationSupport = "unsupported"
	CancellationCooperative     CancellationSupport = "cooperative"
	CancellationProviderRestart CancellationSupport = "provider_restart"
)

// RequestContext is the provider-visible identity and lifetime of one request.
// The Context passed to Provider.Call is authoritative for cancellation; these
// fields make the requested semantics inspectable in traces and fake backends.
type RequestContext struct {
	RequestID     string
	Actor         string
	WorkspaceID   string
	Epoch         uint64
	TransactionID string
	Deadline      time.Time
	Cancellation  CancellationSupport
}

// Request is one transport-independent semantic provider call.
type Request struct {
	Context   RequestContext
	Operation string
	Arguments map[string]any
}

// Result is the common provider result boundary. Value is the operation's own
// reply; the other fields are the kernel's account of the call and are empty
// when the kernel had nothing to report for that operation.
type Result struct {
	Value any
	// Touched lists the documents the call changed or staged, as the kernel
	// saw them when the call completed.
	Touched []DocumentSnapshot
	// Evidence carries diagnostic batches the call collected, in the shape
	// huyang_diagnostic_evidence returns.
	Evidence []EvidenceBatch
	// Health is the kernel's own view of the generation at completion time.
	// State is empty when the kernel reported none.
	Health Health
}

// DocumentSnapshot is the kernel's view of one buffer at completion time. It
// is the shared DocumentSnapshot contract: URI, changedtick, content hash,
// disk fingerprint and dirty state.
type DocumentSnapshot struct {
	URI             string `json:"uri"`
	Path            string `json:"path"`
	ChangedTick     int64  `json:"changedtick"`
	ContentSHA256   string `json:"sha256,omitempty"`
	DiskFingerprint string `json:"disk_fingerprint,omitempty"`
	Dirty           bool   `json:"dirty"`
	Exists          bool   `json:"exists"`
}

// EvidenceRange is a one-based line and character range.
type EvidenceRange struct {
	StartLine      int `json:"start_line"`
	StartCharacter int `json:"start_character"`
	EndLine        int `json:"end_line"`
	EndCharacter   int `json:"end_character"`
}

// EvidenceFinding is one normalized diagnostic inside an EvidenceBatch.
type EvidenceFinding struct {
	Range    EvidenceRange `json:"range"`
	Severity int           `json:"severity"`
	Code     string        `json:"code,omitempty"`
	Source   string        `json:"source,omitempty"`
	Message  string        `json:"message"`
}

// EvidenceBatch is one producer's diagnostic report for one document, in the
// shape the kernel's huyang_diagnostic_evidence operation returns.
type EvidenceBatch struct {
	Kind             string            `json:"kind"`
	ProviderID       string            `json:"provider_id"`
	Producer         string            `json:"producer"`
	ProducerVersion  string            `json:"producer_version,omitempty"`
	Document         string            `json:"document,omitempty"`
	DocumentRevision string            `json:"document_revision,omitempty"`
	DocumentVersion  *int64            `json:"document_version,omitempty"`
	ExpectedVersion  *int64            `json:"expected_version,omitempty"`
	ResultID         string            `json:"result_id,omitempty"`
	TransactionID    string            `json:"transaction_id,omitempty"`
	Complete         bool              `json:"complete"`
	TimedOut         bool              `json:"timed_out,omitempty"`
	Reason           string            `json:"reason,omitempty"`
	ProgressPending  bool              `json:"progress_pending,omitempty"`
	ChangeBarrier    bool              `json:"change_barrier,omitempty"`
	Selected         bool              `json:"selected"`
	Dimension        string            `json:"dimension,omitempty"`
	Findings         []EvidenceFinding `json:"findings,omitempty"`
}

// ProviderError is an operation failure the kernel reported. Error returns
// the message alone so callers matching on kernel wording keep working; Code
// is the stable classification ("lsp_not_configured", "workspace_busy",
// "provider_cancelled", or "lua_error" for an unclassified raise).
type ProviderError struct {
	Code    string
	Message string
	Detail  string
	Epoch   uint64
}

func (e *ProviderError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// ErrorCode returns the ProviderError code inside err, or "" when err is not
// a kernel operation failure.
func ErrorCode(err error) string {
	var operation *ProviderError
	if errors.As(err, &operation) {
		return operation.Code
	}
	return ""
}

// HealthState is the lifecycle state visible to the workspace core.
type HealthState string

const (
	HealthStarting HealthState = "starting"
	HealthHealthy  HealthState = "healthy"
	HealthFailed   HealthState = "failed"
	HealthClosed   HealthState = "closed"
)

// Health is a point-in-time provider lifecycle report.
type Health struct {
	State        HealthState
	Detail       string
	FailureCode  string
	Epoch        uint64
	ObservedAt   time.Time
	Cancellation CancellationSupport
}

// Descriptor contains stable routing facts and compatibility metadata.
type Descriptor struct {
	ID           ID
	Backend      string
	Root         string
	Endpoint     string
	ProcessID    int
	Epoch        uint64
	Cancellation CancellationSupport
	Languages    []string
	Capabilities []Capability
}

// FailureCode classifies provider lifecycle failures for recovery policy.
type FailureCode string

const (
	FailureLaunch       FailureCode = "provider_launch_failed"
	FailureBootstrap    FailureCode = "provider_bootstrap_failed"
	FailureIncompatible FailureCode = "provider_incompatible"
	FailureDied         FailureCode = "provider_died"
	FailureCancelled    FailureCode = "provider_cancelled"
	FailureDeadline     FailureCode = "provider_deadline_exceeded"
	FailureProtocol     FailureCode = "provider_protocol_failed"
)

// Failure is a classified provider error with the generation that observed it.
type Failure struct {
	Code   FailureCode
	Epoch  uint64
	Detail string
	Err    error
}

func (f *Failure) Error() string {
	if f == nil {
		return ""
	}
	if f.Detail != "" {
		return fmt.Sprintf("%s (epoch %d): %s", f.Code, f.Epoch, f.Detail)
	}
	return fmt.Sprintf("%s (epoch %d)", f.Code, f.Epoch)
}

func (f *Failure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Err
}

// Provider is the complete lifecycle and call seam used by the workspace core.
type Provider interface {
	Descriptor() Descriptor
	Health(context.Context) Health
	Call(context.Context, Request) (Result, error)
	Close(context.Context) error
	Done() <-chan struct{}
}

// Registration assigns one provider a role for selected capabilities.
type Registration struct {
	Provider     Provider
	Languages    []string
	Capabilities []Capability
	Role         Role
	FallbackFor  ID
}

// Route is the deterministic route for one capability and language.
type Route struct {
	Primary       Provider
	Complementary []Provider
	Fallbacks     []Provider
}

// AnalysisProfile fixes provider selection for a workspace. Ordinary calls ask
// only for a capability and language; they do not choose a provider.
type AnalysisProfile struct {
	name          string
	registrations []Registration
}

// NewAnalysisProfile validates and freezes a named provider routing profile.
func NewAnalysisProfile(name string, registrations []Registration) (*AnalysisProfile, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("analysis profile needs a name")
	}
	if len(registrations) == 0 {
		return nil, fmt.Errorf("analysis profile %q has no providers", name)
	}
	roles := map[ID]Role{}
	frozen := make([]Registration, len(registrations))
	for i, registration := range registrations {
		if registration.Provider == nil {
			return nil, fmt.Errorf("analysis profile %q registration %d has no provider", name, i)
		}
		id := registration.Provider.Descriptor().ID
		if id == "" {
			return nil, fmt.Errorf("analysis profile %q registration %d has an empty provider ID", name, i)
		}
		if _, exists := roles[id]; exists {
			return nil, fmt.Errorf("analysis profile %q registers provider %q more than once", name, id)
		}
		switch registration.Role {
		case RolePrimary, RoleComplementary, RoleFallback:
		default:
			return nil, fmt.Errorf("analysis profile %q provider %q has invalid role %q", name, id, registration.Role)
		}
		roles[id] = registration.Role
		if len(registration.Capabilities) == 0 {
			registration.Capabilities = append([]Capability(nil), registration.Provider.Descriptor().Capabilities...)
		} else {
			registration.Capabilities = append([]Capability(nil), registration.Capabilities...)
		}
		registration.Languages = append([]string(nil), registration.Languages...)
		frozen[i] = registration
	}
	for _, registration := range frozen {
		if registration.Role == RoleFallback {
			if registration.FallbackFor == "" || roles[registration.FallbackFor] != RolePrimary {
				return nil, fmt.Errorf("analysis profile %q fallback %q names non-primary provider %q",
					name, registration.Provider.Descriptor().ID, registration.FallbackFor)
			}
		} else if registration.FallbackFor != "" {
			return nil, fmt.Errorf("analysis profile %q provider %q is not a fallback",
				name, registration.Provider.Descriptor().ID)
		}
	}
	return &AnalysisProfile{name: name, registrations: frozen}, nil
}

// Name returns the stable profile name.
func (p *AnalysisProfile) Name() string {
	if p == nil {
		return ""
	}
	return p.name
}

// Route selects the profile's provider set without per-call provider routing.
func (p *AnalysisProfile) Route(capability Capability, language string) (Route, error) {
	if p == nil {
		return Route{}, errors.New("no analysis profile")
	}
	var route Route
	var primaryID ID
	for _, registration := range p.registrations {
		if !containsCapability(registration.Capabilities, capability) ||
			!matchesLanguage(registration.Languages, language) {
			continue
		}
		switch registration.Role {
		case RolePrimary:
			if route.Primary != nil {
				return Route{}, fmt.Errorf("analysis profile %q has multiple primaries for %s/%s: %q and %q",
					p.name, language, capability, primaryID, registration.Provider.Descriptor().ID)
			}
			route.Primary = registration.Provider
			primaryID = registration.Provider.Descriptor().ID
		case RoleComplementary:
			route.Complementary = append(route.Complementary, registration.Provider)
		}
	}
	if route.Primary == nil {
		return Route{}, fmt.Errorf("analysis profile %q has no primary for %s/%s", p.name, language, capability)
	}
	for _, registration := range p.registrations {
		if registration.Role == RoleFallback && registration.FallbackFor == primaryID &&
			containsCapability(registration.Capabilities, capability) &&
			matchesLanguage(registration.Languages, language) {
			route.Fallbacks = append(route.Fallbacks, registration.Provider)
		}
	}
	sortProviders(route.Complementary)
	sortProviders(route.Fallbacks)
	return route, nil
}

func containsCapability(capabilities []Capability, want Capability) bool {
	for _, capability := range capabilities {
		if capability == want {
			return true
		}
	}
	return false
}

func matchesLanguage(languages []string, want string) bool {
	if len(languages) == 0 || want == "" {
		return true
	}
	for _, language := range languages {
		if language == "*" || language == want {
			return true
		}
	}
	return false
}

func sortProviders(providers []Provider) {
	sort.SliceStable(providers, func(i, j int) bool {
		return providers[i].Descriptor().ID < providers[j].Descriptor().ID
	})
}

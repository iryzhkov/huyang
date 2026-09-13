package workspace

// What must still be true after the change lands.
//
// A plan says what to do. An invariant says what the result has to satisfy,
// and moves that question inside the transaction: it is evaluated against the
// prepared revision, before anything canonical is written, and its answer is
// bound to the exact revision it was evaluated against.
//
// The four statuses are deliberately four and not two. "Proven" and
// "violated" are claims about the code; "unknown" is a claim about the
// evidence, and it is the one that matters most, because an invariant nobody
// could evaluate is not an invariant that holds. A required invariant that is
// unknown stops a plan reaching READY for the same reason a violated one
// does.
//
// Nothing here is implicit. A plan with no declared invariants has no
// requirements, which is what makes it safe to read a plan record written
// before invariants existed.

import (
	"fmt"
	"strings"
	"time"
)

// planInvariantLimit bounds how many invariants one plan may declare. The
// limit exists because every one of them is evaluated on every prepare.
const planInvariantLimit = 16

// InvariantKind is what an invariant asserts. Each kind names the evidence
// that can settle it, and no kind is settled by anything weaker.
type InvariantKind string

const (
	// InvariantNoNewDiagnostics: the proposal introduces no diagnostic the
	// workspace did not already have. Settled by the prepared diagnostic
	// delta, and unknown whenever the canonical baseline is incomplete.
	InvariantNoNewDiagnostics InvariantKind = "no_new_diagnostics"
	// InvariantTestsPass: the prepared revision's test stage passed. Settled
	// by the verification the sandbox pipeline already ran.
	InvariantTestsPass InvariantKind = "tests_pass"
	// InvariantNoReferences: nothing outside the declaration still refers to
	// the named symbol. Settled by the language server reading the staged
	// bytes, and unknown without one.
	InvariantNoReferences InvariantKind = "no_references"
	// InvariantSymbolExists and InvariantSymbolAbsent: the named declaration
	// is, or is not, in the staged file.
	InvariantSymbolExists InvariantKind = "symbol_exists"
	InvariantSymbolAbsent InvariantKind = "symbol_absent"
	// InvariantAPICompatible: the proposal breaks nothing that was exported.
	// Settled by reading each staged file's exported surface before and after
	// with the adapter for its language, and unknown for a file no adapter
	// covers.
	InvariantAPICompatible InvariantKind = "api_compatible"
	// InvariantPathUnreachable: nothing can still reach the named declaration
	// after the change. This one can be refuted and not yet proved: refuting
	// it takes one reference, proving it takes a complete static execution
	// graph, which this service does not build.
	InvariantPathUnreachable InvariantKind = "path_unreachable"
)

// InvariantEnforcement decides what an unproven invariant costs. Required is
// the default: declaring an invariant is asking for it to hold.
type InvariantEnforcement string

const (
	InvariantRequired InvariantEnforcement = "required"
	InvariantAdvisory InvariantEnforcement = "advisory"
)

// InvariantStatus separates what is known from what is true.
type InvariantStatus string

const (
	// InvariantPending: declared, not yet evaluated. Every invariant is
	// pending until a prepare evaluates it, and returns to pending when the
	// plan is edited.
	InvariantPending  InvariantStatus = "pending"
	InvariantProven   InvariantStatus = "proven"
	InvariantViolated InvariantStatus = "violated"
	// InvariantUnknown: nobody could answer. Not a pass.
	InvariantUnknown InvariantStatus = "unknown"
)

// InvariantScope are the typed parameters of one invariant. Which fields an
// invariant needs depends on its kind, and normalizeInvariants refuses a
// declaration that leaves out what its kind cannot work without.
type InvariantScope struct {
	// Paths narrows a diagnostic or test assertion to these workspace paths;
	// empty means every file the plan stages.
	Paths []string `json:"paths,omitempty"`
	// Symbol is the declaration a symbol assertion is about.
	Symbol *PlanSymbolLocator `json:"symbol,omitempty"`
	// Severity is the weakest finding that counts as new for
	// no_new_diagnostics: "error", "warning", "info" or "hint". Empty counts
	// every finding.
	Severity string `json:"severity,omitempty"`
}

// PlanInvariant is one durable assertion about a plan's result, together with
// everything needed to judge whether its answer is still worth anything: the
// revision it was evaluated against, how complete that evidence was, and
// which evidence records back it.
type PlanInvariant struct {
	ID          string               `json:"id"`
	Kind        InvariantKind        `json:"kind"`
	Enforcement InvariantEnforcement `json:"enforcement"`
	Scope       InvariantScope       `json:"scope,omitempty"`
	Status      InvariantStatus      `json:"status"`
	// EvaluatedRevision is the prepared revision this answer describes. A
	// proof that names another revision is not a proof of this one.
	EvaluatedRevision string    `json:"evaluated_revision,omitempty"`
	Coverage          Coverage  `json:"coverage,omitempty"`
	EvidenceIDs       []string  `json:"evidence_ids,omitempty"`
	Detail            string    `json:"detail,omitempty"`
	EvaluatedAt       time.Time `json:"evaluated_at,omitempty"`
}

// normalizeInvariants validates one declaration list and returns it in the
// shape a plan record stores. A caller-supplied status is discarded: an
// invariant is proven by evaluating it, never by asking for it to be proven.
func normalizeInvariants(invariants []PlanInvariant) ([]PlanInvariant, error) {
	if len(invariants) == 0 {
		return nil, nil
	}
	if len(invariants) > planInvariantLimit {
		return nil, Codedf(CodePlanValidationConflicts,
			"a plan may declare at most %d invariants; this one declares %d", planInvariantLimit, len(invariants))
	}
	seen := make(map[string]bool, len(invariants))
	normalized := make([]PlanInvariant, 0, len(invariants))
	for index, invariant := range invariants {
		invariant.ID = strings.TrimSpace(invariant.ID)
		if invariant.ID == "" {
			invariant.ID = fmt.Sprintf("inv_%d", index+1)
		}
		if seen[invariant.ID] {
			return nil, Codedf(CodePlanValidationConflicts, "invariant %s is declared twice", invariant.ID)
		}
		seen[invariant.ID] = true
		switch invariant.Enforcement {
		case "":
			invariant.Enforcement = InvariantRequired
		case InvariantRequired, InvariantAdvisory:
		default:
			return nil, Codedf(CodePlanValidationConflicts,
				"invariant %s has enforcement %q; it is required or advisory", invariant.ID, invariant.Enforcement)
		}
		if err := validateInvariantScope(invariant); err != nil {
			return nil, err
		}
		invariant.Status, invariant.EvaluatedRevision = InvariantPending, ""
		invariant.Coverage, invariant.EvidenceIDs = Coverage{}, nil
		invariant.Detail, invariant.EvaluatedAt = "", time.Time{}
		normalized = append(normalized, invariant)
	}
	return normalized, nil
}

// validateInvariantScope refuses a declaration whose kind cannot be evaluated
// from what it was given, because the alternative is an invariant that is
// permanently unknown and blocks every prepare without saying why.
func validateInvariantScope(invariant PlanInvariant) error {
	switch invariant.Kind {
	case InvariantNoNewDiagnostics, InvariantTestsPass, InvariantAPICompatible:
		return nil
	case InvariantNoReferences, InvariantSymbolExists, InvariantSymbolAbsent, InvariantPathUnreachable:
		symbol := invariant.Scope.Symbol
		if symbol == nil || strings.TrimSpace(symbol.Path) == "" || strings.TrimSpace(symbol.NamePath) == "" {
			return Codedf(CodePlanValidationConflicts,
				"invariant %s (%s) needs scope.symbol with a path and a name_path", invariant.ID, invariant.Kind)
		}
		return nil
	default:
		return Codedf(CodePlanValidationConflicts, "invariant %s has unknown kind %q", invariant.ID, invariant.Kind)
	}
}

// resetInvariantProofs returns the declarations with every answer dropped.
// Editing a plan changes the bytes the answers were about, so keeping them
// would be keeping a proof of something else.
func resetInvariantProofs(invariants []PlanInvariant) []PlanInvariant {
	if len(invariants) == 0 {
		return nil
	}
	reset := make([]PlanInvariant, 0, len(invariants))
	for _, invariant := range invariants {
		invariant.Status, invariant.EvaluatedRevision = InvariantPending, ""
		invariant.Coverage, invariant.EvidenceIDs = Coverage{}, nil
		invariant.Detail, invariant.EvaluatedAt = "", time.Time{}
		reset = append(reset, invariant)
	}
	return reset
}

// cloneInvariants copies a declaration list deeply enough that a caller
// cannot reach into a stored plan through it.
func cloneInvariants(invariants []PlanInvariant) []PlanInvariant {
	if len(invariants) == 0 {
		return nil
	}
	cloned := make([]PlanInvariant, 0, len(invariants))
	for _, invariant := range invariants {
		invariant.Scope.Paths = append([]string(nil), invariant.Scope.Paths...)
		if invariant.Scope.Symbol != nil {
			symbol := *invariant.Scope.Symbol
			invariant.Scope.Symbol = &symbol
		}
		invariant.EvidenceIDs = append([]string(nil), invariant.EvidenceIDs...)
		invariant.Coverage.Skipped = append([]string(nil), invariant.Coverage.Skipped...)
		cloned = append(cloned, invariant)
	}
	return cloned
}

// provenAt reports whether this invariant was proven, and proven about the
// revision being asked about rather than some earlier preparation of the same
// plan.
func (i PlanInvariant) provenAt(preparedRevision string) bool {
	return i.Status == InvariantProven && preparedRevision != "" && i.EvaluatedRevision == preparedRevision
}

// describe is what an invariant says about itself in a refusal or a gap.
func (i PlanInvariant) describe() string {
	detail := strings.TrimSpace(i.Detail)
	if detail == "" {
		detail = string(i.Status)
	}
	return fmt.Sprintf("%s (%s): %s", i.ID, i.Kind, detail)
}

// unprovenInvariants splits the declarations that are not proven about this
// prepared revision by what their enforcement costs.
func unprovenInvariants(invariants []PlanInvariant, preparedRevision string) (required, advisory []PlanInvariant) {
	for _, invariant := range invariants {
		if invariant.provenAt(preparedRevision) {
			continue
		}
		if invariant.Enforcement == InvariantAdvisory {
			advisory = append(advisory, invariant)
			continue
		}
		required = append(required, invariant)
	}
	return required, advisory
}

// invariantGaps are the advisory invariants a committer must accept
// explicitly, in the same shape as every other incomplete verification
// dimension.
func invariantGaps(invariants []PlanInvariant, preparedRevision string) []VerificationGap {
	_, advisory := unprovenInvariants(invariants, preparedRevision)
	var gaps []VerificationGap
	for _, invariant := range advisory {
		gaps = append(gaps, VerificationGap{
			Dimension: "invariant:" + invariant.ID, Detail: invariant.describe(),
		})
	}
	return gaps
}

// mergeInvariantResults folds one evaluation back onto the declarations by ID.
// A declaration the evaluator said nothing about stays unknown rather than
// inheriting an older answer, because silence is not evidence.
func mergeInvariantResults(declared, evaluated []PlanInvariant, preparedRevision string, at time.Time) []PlanInvariant {
	answers := make(map[string]PlanInvariant, len(evaluated))
	for _, answer := range evaluated {
		answers[answer.ID] = answer
	}
	merged := make([]PlanInvariant, 0, len(declared))
	for _, invariant := range declared {
		answer, found := answers[invariant.ID]
		if !found {
			invariant.Status, invariant.EvaluatedRevision = InvariantUnknown, preparedRevision
			invariant.Detail, invariant.EvaluatedAt = "nothing evaluated this invariant", at
			merged = append(merged, invariant)
			continue
		}
		invariant.Status = answer.Status
		invariant.EvaluatedRevision = preparedRevision
		invariant.Coverage, invariant.EvidenceIDs = answer.Coverage, answer.EvidenceIDs
		invariant.Detail, invariant.EvaluatedAt = answer.Detail, at
		merged = append(merged, invariant)
	}
	return merged
}

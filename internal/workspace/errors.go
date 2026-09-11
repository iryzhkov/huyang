package workspace

import (
	"errors"
	"fmt"
)

// Stable error codes emitted by the plan, commit and recovery lifecycle. The bridge maps
// these onto tool-result codes; callers should classify with ErrorCode rather than by
// matching error text.
const (
	CodeCommitPreconditionChanged = "commit_precondition_changed"
	CodeCommitRecoveryRequired    = "commit_recovery_required"
	CodeWorkspaceEpochChanged     = "workspace_epoch_changed"
	CodePreparedRevisionChanged   = "prepared_revision_changed"
	CodePlanRevisionChanged       = "plan_revision_changed"
	CodePlanStateInvalid          = "plan_state_invalid"
	CodePlanValidationConflicts   = "plan_validation_conflicts"
	CodeProvisionalNotAccepted    = "provisional_not_accepted"
	CodeProviderUnavailable       = "provider_unavailable"
	CodeWorkspaceBusy             = "workspace_busy"
)

// CodedError carries a stable machine-readable code next to a human-readable cause.
// Its text keeps the historical "code: detail" shape so callers that still classify by
// substring keep working while they migrate to ErrorCode.
type CodedError struct {
	Code string
	Err  error
}

func (e *CodedError) Error() string {
	if e.Err == nil {
		return e.Code
	}
	return e.Code + ": " + e.Err.Error()
}

func (e *CodedError) Unwrap() error { return e.Err }

// Coded wraps err under code. A nil err yields an error whose text is the bare code.
func Coded(code string, err error) error {
	return &CodedError{Code: code, Err: err}
}

// Codedf formats a detail message under code; %w verbs wrap as with fmt.Errorf.
func Codedf(code, format string, args ...any) error {
	return &CodedError{Code: code, Err: fmt.Errorf(format, args...)}
}

// ErrorCode returns the stable code of the outermost CodedError in err's chain, or an
// empty string when err carries no code.
func ErrorCode(err error) string {
	var coded *CodedError
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

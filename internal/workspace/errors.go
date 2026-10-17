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
	CodePlanNotFound              = "plan_not_found"
	CodeHandleUnknown             = "handle_unknown"
	CodeHandleExpired             = "handle_expired"
	CodePlanValidationConflicts   = "plan_validation_conflicts"
	CodeProvisionalNotAccepted    = "provisional_not_accepted"
	CodeInvariantNotProven        = "invariant_not_proven"
	CodeProviderUnavailable       = "provider_unavailable"
	CodeWorkspaceBusy             = "workspace_busy"
	CodeDocumentNotFound          = "document_not_found"
)

// DocumentNotFoundError is the cause under CodeDocumentNotFound: a read of a
// path at which nothing exists. Path is workspace-relative, so a reply can
// name it the way the caller did rather than as an absolute path on a host
// the caller may not share.
type DocumentNotFoundError struct {
	Path string
}

func (e *DocumentNotFoundError) Error() string { return "no file at " + e.Path }

// NotRegularTextError is a read of a path that exists but is not a regular
// text file: a directory, a symlink or a binary. The handler that meets one
// can answer what the caller wanted - a listing for a directory, the
// resolved target for a symlink - instead of the bare refusal; the text is
// unchanged from the plain error it replaces.
type NotRegularTextError struct {
	// Path is workspace-relative, or absolute for the workspace root itself.
	Path string
	// Absolute is the confined absolute path that was inspected.
	Absolute string
	Kind     ObjectKind
}

func (e *NotRegularTextError) Error() string {
	return fmt.Sprintf("%s is %s, not regular text", e.Absolute, e.Kind)
}

// SymlinkEscapeError is a path that passes through a symbolic link whose
// target leaves the workspace root. The guard that raises it stays; the
// fields let a reply name where the link leads, which is a file the caller
// can still read as a document of its own.
type SymlinkEscapeError struct {
	Document string
	Link     string
	Root     string
}

func (e *SymlinkEscapeError) Error() string {
	return fmt.Sprintf("document %s passes through symlink %s that leaves workspace root %s", e.Document, e.Link, e.Root)
}

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

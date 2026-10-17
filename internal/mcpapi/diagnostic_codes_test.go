package mcpapi

import (
	"reflect"
	"strings"
	"testing"
)

func TestDiagnosticReasonCodesDoNotExportProviderText(t *testing.T) {
	reasons := []string{"python_environment_unresolved", "push_missing_current_document_proof", "push_missing_current_document_proof", "/home/private/token", "provider exposed secret", "code_shaped_secret", "", strings.Repeat("a", 100)}
	reasons = append(reasons, make([]string, 64)...)
	got := DiagnosticReasonCodes(reasons)
	want := []string{"diagnostic_reason_unclassified", "push_missing_current_document_proof", "python_environment_unresolved"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reason codes: %v", got)
	}
	if len(got) > 16 {
		t.Fatalf("unbounded codes: %v", got)
	}
	if len(DiagnosticReasonCodes(nil)) != 0 {
		t.Fatal("invented a reason for an empty report")
	}
}

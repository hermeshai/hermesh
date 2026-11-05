package cli

import (
	"testing"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// A failure kind or status newer than this build (a newer machine's) reads
// "unknown", one of the documented names, never its number.
func TestNamesFromANewerMachine(t *testing.T) {
	if got := failureName(sessionv1.FailureKind_FAILURE_KIND_BASE_REF_UNAVAILABLE); got != "base_ref_unavailable" {
		t.Fatalf("known kind: %q", got)
	}
	if got := failureName(sessionv1.FailureKind(999)); got != "unknown" {
		t.Fatalf("newer kind: %q", got)
	}
	if got := statusName(sessionv1.SessionStatus(999)); got != "unknown" {
		t.Fatalf("newer status: %q", got)
	}
}

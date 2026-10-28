package cloudinfra

import (
	"fmt"
	"strings"
	"testing"
)

// A capacity block is a conflict too, but the failure says the policy
// stopped the server, not that the reviewed connection changed.
func TestSafeFailureNamesACapacityBlock(t *testing.T) {
	blocked := fmt.Errorf("execute: %w", &CapacityAdmissionError{Block: CapacityBlock{Reason: "hard_limit", Resource: "gpus:nvidia-l40s"}})
	got := safeFailure(blocked)
	if !strings.HasPrefix(got, "The cloud's capacity policy blocked this server before it was created") || !strings.Contains(got, "hard_limit on gpus:nvidia-l40s") {
		t.Errorf("capacity block: %q", got)
	}
	if got = safeFailure(fmt.Errorf("x: %w", ErrConflict)); !strings.HasPrefix(got, "The reviewed connection or server changed.") {
		t.Errorf("conflict: %q", got)
	}
}

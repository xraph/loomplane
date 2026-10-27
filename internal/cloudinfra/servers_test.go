package cloudinfra

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// A refusal beside a direct deployment still maps to 409 through ErrBusy,
// but says what's running rather than "infrastructure operation".
func TestErrDirectApplyRunning(t *testing.T) {
	err := fmt.Errorf("save pool: %w", ErrDirectApplyRunning)
	if !errors.Is(err, ErrBusy) || !errors.Is(err, ErrDirectApplyRunning) {
		t.Fatalf("errors.Is lost the sentinel: %v", err)
	}
	if errors.Is(ErrBusy, ErrDirectApplyRunning) {
		t.Fatal("every busy operation reads as a direct deployment")
	}
	if !strings.HasPrefix(ErrDirectApplyRunning.Error(), "a direct deployment is running on this server") {
		t.Fatalf("message: %q", ErrDirectApplyRunning.Error())
	}
}

package firecracker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// smtActive reports whether simultaneous multithreading is on, from the kernel's
// smt directory. A missing active file counts as off only when control says the
// CPU does not support SMT; any other unreadable state is an error.
func smtActive(root string) (bool, error) {
	raw, err := os.ReadFile(filepath.Join(root, "active")) //nolint:gosec // Fixed kernel path, or a test directory.
	if err == nil {
		switch strings.TrimSpace(string(raw)) {
		case "0":
			return false, nil
		case "1":
			return true, nil
		}
		return false, fmt.Errorf("%w: SMT state is unreadable", ErrUnavailable)
	}
	control, cerr := os.ReadFile(filepath.Join(root, "control")) //nolint:gosec // Fixed kernel path, or a test directory.
	if cerr == nil && strings.TrimSpace(string(control)) == "notsupported" {
		return false, nil
	}
	return false, fmt.Errorf("%w: SMT state is unreadable", ErrUnavailable)
}

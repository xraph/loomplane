//go:build integration

package integration

import (
	"errors"
	"testing"
)

// cleanupCloudAcceptance drains provider-mutating work before disposal. Both
// callbacks return errors so shutdown failure cannot prevent external cleanup.
func cleanupCloudAcceptance(stop, dispose func() error) error {
	stopErr := stop()
	disposeErr := dispose()
	return errors.Join(stopErr, disposeErr)
}

func TestCloudAcceptanceCleanupDrainsBeforeDisposal(t *testing.T) {
	for _, shutdownFails := range []bool{false, true} {
		name := "successful shutdown"
		if shutdownFails {
			name = "shutdown error"
		}
		t.Run(name, func(t *testing.T) {
			drained := make(chan struct{})
			stopWork := make(chan struct{})
			projectExists := false
			go func() { <-stopWork; projectExists = true; close(drained) }()
			shutdownErr := errors.New("shutdown reported an error after drain")
			disposed := false
			err := cleanupCloudAcceptance(func() error {
				close(stopWork)
				<-drained
				if shutdownFails {
					return shutdownErr
				}
				return nil
			}, func() error {
				disposed = true
				projectExists = false
				return nil
			})
			if !disposed || projectExists {
				t.Fatal("provider work created a project after disposal")
			}
			if errors.Is(err, shutdownErr) != shutdownFails {
				t.Fatalf("shutdown error lost: %v", err)
			}
		})
	}
}

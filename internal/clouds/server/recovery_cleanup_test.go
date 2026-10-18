package server

import (
	"errors"
	"testing"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

func TestRecoveryCleanupRejectsLiveInstanceBeforeOpeningTarget(t *testing.T) {
	r := &Runtime{}
	for _, labels := range []map[string]string{{owned.LabelPublic: "false"}, {LabelRecovery: "restore", owned.LabelPublic: "true"}} {
		if err := r.PurgeRecovery(t.Context(), id.ID{}, labels); !errors.Is(err, ErrOwned) {
			t.Fatalf("cleanup authorization: %v", err)
		}
	}
}

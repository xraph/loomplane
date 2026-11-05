package metering

import (
	"testing"
	"time"
)

func TestNoopsAllowAndDiscard(t *testing.T) {
	ctx := t.Context()
	for _, k := range []Kind{KindDeploy, KindBuild, KindAIPlatform} {
		if err := AllowAll.CanRun(ctx, "org", k, true); err != nil {
			t.Fatalf("CanRun %s: %v", k, err)
		}
	}
	e := Event{ID: "e1", OrgID: "org", Feature: "vm", Quantity: 1, OccurredAt: time.Now()}
	for range 2 {
		if err := Discard.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
}

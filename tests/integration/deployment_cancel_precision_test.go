//go:build integration

package integration

import (
	"testing"
	"time"
)

func TestDeploymentCancelPersistsResponseTime(t *testing.T) {
	a := capacityAPISetup(t)
	a.runningDeployment(t, a.org, "dep-precision")
	// Keep nanoseconds explicit: the host clock may only supply microseconds.
	at := time.Date(2026, time.November, 4, 12, 0, 0, 123456789, time.UTC)
	first, err := a.deploys.CancelDeployment(t.Context(), a.org, "dep-precision", at)
	if err != nil {
		t.Fatal(err)
	}
	again, err := a.deploys.CancelDeployment(t.Context(), a.org, "dep-precision", at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := a.deploys.GetDeployment(t.Context(), a.org, "dep-precision")
	if err != nil {
		t.Fatal(err)
	}
	if first.CancelRequestedAt == nil || again.CancelRequestedAt == nil || stored.CancelRequestedAt == nil ||
		!first.CancelRequestedAt.Equal(*again.CancelRequestedAt) || !first.CancelRequestedAt.Equal(*stored.CancelRequestedAt) {
		t.Fatalf("cancel response changed after persistence: first=%v again=%v stored=%v", first.CancelRequestedAt, again.CancelRequestedAt, stored.CancelRequestedAt)
	}
}

package cloudinfra

import (
	"fmt"
	"strings"
	"testing"
)

// A capacity block is a conflict too, but the failure says the policy
// stopped the server, not that the reviewed connection changed, in words
// and never in codes.
func TestSafeFailureNamesACapacityBlock(t *testing.T) {
	blocked := fmt.Errorf("execute: %w", &CapacityAdmissionError{Block: CapacityBlock{Reason: "hard_limit", Resource: "gpus:nvidia-l40s", Limit: 2, Counted: 2, Requested: 1}})
	got := safeFailure(blocked)
	want := "The cloud's capacity policy blocked this server before it was created: it would pass the limit of 2 NVIDIA L40S GPUs (2 counted, 1 more needed). Change the policy or free capacity, then retry."
	if got != want {
		t.Errorf("capacity block: %q", got)
	}
	if strings.Contains(got, "hard_limit") || strings.Contains(got, "gpus:") {
		t.Errorf("capacity block shows a code: %q", got)
	}
	if got = safeFailure(fmt.Errorf("x: %w", ErrConflict)); !strings.HasPrefix(got, "The reviewed connection or server changed.") {
		t.Errorf("conflict: %q", got)
	}
}

func TestCapacityBlockSummaries(t *testing.T) {
	for _, c := range []struct {
		block CapacityBlock
		want  string
	}{
		{CapacityBlock{Reason: "hard_limit", Resource: "gpus:nvidia-h100-80gb", Requested: 1}, "it has no ceiling for NVIDIA H100 80GB GPUs, so it allows none"},
		{CapacityBlock{Reason: "hard_limit", Resource: "servers", Limit: 3, Counted: 3, Requested: 1}, "it would pass the limit of 3 servers (3 counted, 1 more needed)"},
		{CapacityBlock{Reason: "provider_quota", Resource: "cpu_millis", Limit: 8000, Counted: 8000, Requested: 2000}, "the provider account's quota of 8000 CPU millicores is full (8000 counted, 2000 more needed)"},
		{CapacityBlock{Reason: "stale_inventory", Resource: "inventory"}, "its count of servers and their resources is out of date"},
		{CapacityBlock{Reason: "stale_inventory", Resource: "gpus:some-new-gpu"}, "its count of some-new-gpu GPUs is out of date"},
		{CapacityBlock{Reason: "review_required", Resource: "policy"}, "it waits for an admin to approve it"},
		{CapacityBlock{Reason: "placement_constraint", Resource: "pool"}, "no pool can place it"},
		{CapacityBlock{Reason: "something_new", Resource: "rollout"}, "it holds servers and their resources back"},
	} {
		if got := c.block.Summary(); got != c.want {
			t.Errorf("%s on %s: %q", c.block.Reason, c.block.Resource, got)
		}
	}
}

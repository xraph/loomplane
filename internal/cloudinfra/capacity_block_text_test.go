package cloudinfra

import "testing"

func TestCapacityBlockSentence(t *testing.T) {
	for _, tc := range []struct {
		block CapacityBlock
		want  string
	}{
		{CapacityBlock{Reason: "hard_limit", Resource: "cpu_millis", Counted: 4000, Limit: 8000, Requested: 500}, "CPU is at this Cloud's limit: 4 vCPU of 8 vCPU counted, 0.5 vCPU more needed."},
		{CapacityBlock{Reason: "hard_limit", Resource: "memory_bytes", PoolID: "p", Counted: 6 << 30, Limit: 8 << 30, Requested: 512 << 20}, "Memory is at this pool's limit: 6 GiB of 8 GiB counted, 512 MiB more needed."},
		{CapacityBlock{Reason: "hard_limit", Resource: "gpus:nvidia-l4", Counted: 2, Limit: 2, Requested: 1}, "NVIDIA L4 GPUs is at this Cloud's limit: 2 of 2 counted, 1 more needed."},
		{CapacityBlock{Reason: "review_required", Resource: "rollout"}, RolloutOffText},
		{CapacityBlock{Reason: "review_required"}, "This deployment's plan changed. Review it again."},
		{CapacityBlock{Reason: "stale_inventory", Resource: "servers"}, "Loomplane can't confirm current Servers usage, so it won't add capacity until a refresh succeeds."},
		{CapacityBlock{Reason: "weird_thing"}, "weird thing"},
	} {
		if got := tc.block.Sentence(); got != tc.want {
			t.Errorf("%+v: got %q, want %q", tc.block, got, tc.want)
		}
	}
}

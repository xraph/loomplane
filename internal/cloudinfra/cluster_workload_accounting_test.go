package cloudinfra

import "testing"

func TestClusterWorkloadAccountingRetainsReviewedMaximum(t *testing.T) {
	maximum := ClusterWorkloadVector{CPUMillis: 4000, MemoryBytes: 8000, Pods: 4}
	observed := ClusterWorkloadVector{CPUMillis: 3000, MemoryBytes: 6000, Pods: 3}
	matched := ClusterWorkloadVector{CPUMillis: 1000, MemoryBytes: 2000, Pods: 1}
	used, err := clusterWorkloadUsage(observed, []clusterWorkloadCoverage{{Maximum: maximum, Observed: matched}})
	if err != nil || used != (ClusterWorkloadVector{CPUMillis: 6000, MemoryBytes: 12000, Pods: 6}) {
		t.Fatalf("partial release lost its reviewed remainder: %+v %v", used, err)
	}
	// Recomputing coverage must never accumulate credit across snapshots.
	again, err := clusterWorkloadUsage(observed, []clusterWorkloadCoverage{{Maximum: maximum, Observed: matched}})
	if err != nil || again != used {
		t.Fatalf("replay changed usage: %+v %v", again, err)
	}
}

func TestClusterWorkloadAccountingBounds(t *testing.T) {
	for _, test := range []struct {
		name                       string
		observed, maximum, matched ClusterWorkloadVector
		want                       int64
		invalid                    bool
	}{
		{name: "unknown-proof", observed: ClusterWorkloadVector{CPUMillis: 500}, maximum: ClusterWorkloadVector{CPUMillis: 1000}, want: 1500},
		{name: "excess-observed", observed: ClusterWorkloadVector{CPUMillis: 2000}, maximum: ClusterWorkloadVector{CPUMillis: 1000}, matched: ClusterWorkloadVector{CPUMillis: 2000}, want: 2000},
		{name: "negative", maximum: ClusterWorkloadVector{CPUMillis: -1}, invalid: true},
		{name: "overflow", observed: ClusterWorkloadVector{CPUMillis: 9223372036854775807}, maximum: ClusterWorkloadVector{CPUMillis: 1}, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := clusterWorkloadUsage(test.observed, []clusterWorkloadCoverage{{Maximum: test.maximum, Observed: test.matched}})
			if (err != nil) != test.invalid || err == nil && got.CPUMillis != test.want {
				t.Fatalf("usage %+v %v", got, err)
			}
		})
	}
}

func TestClusterWorkloadRevisionBoundary(t *testing.T) {
	for _, test := range []struct {
		revision string
		valid    bool
	}{
		{"deployment/service:api", true}, {"deployment/service:api/0123456789abcdef", true},
		{"deployment/service:api-other", false}, {"deployment/service:api/", false}, {"other/service:api", false},
	} {
		if got := workloadRevisionMatches(test.revision, "deployment", "service:api"); got != test.valid {
			t.Fatalf("revision %q accepted=%v", test.revision, got)
		}
	}
}

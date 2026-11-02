package releasehealth

import (
	"encoding/json"
	"strings"
	"testing"
)

func h(req, errs, lat float64, stat string) Health {
	return Health{Requests: req, Errors: errs, LatencyMS: lat, Statistic: stat, Available: true}
}

func TestJudgeTable(t *testing.T) {
	th := DefaultThresholds()
	cases := []struct {
		name      string
		baseline  Health
		candidate Health
		want      Verdict
		contains  []string
	}{
		{"candidate unavailable", h(1000, 0, 0, ""), Health{Reason: "x"}, Inconclusive, []string{"candidate", "x"}},
		{"baseline unavailable", Health{Reason: "y"}, h(1000, 0, 0, ""), Inconclusive, []string{"baseline"}},
		{"candidate too few requests", h(1000, 0, 0, ""), h(199, 0, 0, ""), Inconclusive, []string{"199", "200"}},
		{"error ceiling exceeded", h(1000, 0, 0, ""), h(1000, 11, 0, ""), Fail, []string{"1.1%", "1%"}},
		{"error ceiling inclusive", h(1000, 9, 0, ""), h(1000, 10, 0, ""), Pass, nil},
		{"error delta exceeded", h(1000, 1, 0, ""), h(1000, 7, 0, ""), Fail, []string{"baseline"}},
		{"latency ratio exceeded", h(1000, 0, 100, "p95"), h(1000, 0, 131, "p95"), Fail, []string{"1.31"}},
		{"latency ratio at limit", h(1000, 0, 100, "p95"), h(1000, 0, 130, "p95"), Pass, nil},
		{"same statistic compares", h(1000, 0, 100, "average"), h(1000, 0, 200, "average"), Fail, nil},
		{"mixed statistics skip latency", h(1000, 0, 100, "p95"), h(1000, 0, 900, ""), Pass, nil},
		{"thin baseline uses ceiling only", h(50, 0, 0, ""), h(1000, 5, 0, ""), Pass, []string{"ceiling only"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, reason := Judge(c.baseline, c.candidate, th)
			if v != c.want {
				t.Fatalf("verdict = %s (%q), want %s", v, reason, c.want)
			}
			for _, s := range c.contains {
				if !strings.Contains(reason, s) {
					t.Errorf("reason %q missing %q", reason, s)
				}
			}
		})
	}
}

func TestJudgePassReason(t *testing.T) {
	v, reason := Judge(h(1000, 1, 110, "p95"), h(1000, 2, 120, "p95"), DefaultThresholds())
	want := "Passed: 0.2% errors (baseline 0.1%), p95 120ms (baseline 110ms)."
	if v != Pass || reason != want {
		t.Fatalf("got %s %q, want pass %q", v, reason, want)
	}
}

func TestJudgeZeroThresholdsDisableChecks(t *testing.T) {
	v, reason := Judge(h(100, 0, 10, "p95"), h(100, 50, 1000, "p95"), Thresholds{MinRequests: 10})
	if v != Pass {
		t.Fatalf("verdict = %s (%q), want pass", v, reason)
	}
	v, _ = Judge(h(1, 0, 0, ""), h(1, 0, 0, ""), Thresholds{})
	if v != Pass {
		t.Fatalf("MinRequests 0 should behave as 1, got %s", v)
	}
	v, _ = Judge(h(1, 0, 0, ""), h(0, 0, 0, ""), Thresholds{})
	if v != Inconclusive {
		t.Fatalf("zero requests should be inconclusive, got %s", v)
	}
}

func TestErrorRateOfNothingIsZero(t *testing.T) {
	if got := (Health{}).ErrorRate(); got != 0 {
		t.Fatalf("ErrorRate = %v, want 0", got)
	}
}

func TestHealthJSON(t *testing.T) {
	b, err := json.Marshal(Health{Requests: 1, Errors: 0, LatencyMS: 2, Statistic: "p95", Available: true})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"requests":1,"errors":0,"latency_ms":2,"statistic":"p95","available":true}`
	if string(b) != want {
		t.Fatalf("got %s, want %s", b, want)
	}
}

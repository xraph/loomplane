package releasehealth

import (
	"fmt"
	"strconv"
)

// Judge compares a candidate's health with the baseline's. It checks, in order,
// and stops at the first check that decides: candidate availability, baseline
// availability, candidate volume, the error ceiling, then (only when the
// baseline served enough requests) the error-rate delta and latency ratio. A
// threshold is breached only when strictly exceeded.
func Judge(baseline, candidate Health, t Thresholds) (verdict Verdict, reason string) {
	if !candidate.Available {
		return Inconclusive, "The candidate's health isn't available: " + reasonOr(candidate.Reason) + "."
	}
	if !baseline.Available {
		return Inconclusive, "The baseline's health isn't available: " + reasonOr(baseline.Reason) + "."
	}
	minReq := t.MinRequests
	if minReq < 1 {
		minReq = 1
	}
	if candidate.Requests < minReq {
		return Inconclusive, fmt.Sprintf("The candidate served %s requests; at least %s are needed to judge it.", num(candidate.Requests), num(minReq))
	}
	rate := candidate.ErrorRate()
	if t.ErrorRate > 0 && rate > t.ErrorRate {
		return Fail, fmt.Sprintf("%s of the candidate's requests failed; the ceiling is %s.", pct(rate), pctCeil(t.ErrorRate))
	}

	relative := baseline.Requests >= minReq
	if !relative {
		return Pass, fmt.Sprintf("Passed: %s errors, compared with the error ceiling only, because the baseline served too few requests.", pct(rate))
	}
	baseRate := baseline.ErrorRate()
	if t.ErrorRateDelta > 0 && rate-baseRate > t.ErrorRateDelta {
		return Fail, fmt.Sprintf("The candidate's error rate is %s, %s above the baseline's %s; the allowed difference is %s.",
			pct(rate), pct(rate-baseRate), pct(baseRate), pctCeil(t.ErrorRateDelta))
	}
	passed := fmt.Sprintf("Passed: %s errors (baseline %s)", pct(rate), pct(baseRate))
	if t.P95LatencyRatio > 0 && candidate.Statistic != "" && candidate.Statistic == baseline.Statistic && baseline.LatencyMS > 0 {
		ratio := candidate.LatencyMS / baseline.LatencyMS
		if ratio > t.P95LatencyRatio {
			return Fail, fmt.Sprintf("The candidate's %s latency is %sms, %.2f times the baseline's %sms; the ceiling is %s times.",
				candidate.Statistic, num(candidate.LatencyMS), ratio, num(baseline.LatencyMS), num(t.P95LatencyRatio))
		}
		passed += fmt.Sprintf(", %s %sms (baseline %sms)", candidate.Statistic, num(candidate.LatencyMS), num(baseline.LatencyMS))
	}
	return Pass, passed + "."
}

func reasonOr(r string) string {
	if r == "" {
		return "no reason given"
	}
	return r
}

func pct(f float64) string { return strconv.FormatFloat(f*100, 'f', 1, 64) + "%" }

// pctCeil renders a threshold without trailing zeros: 1%, 0.5%.
func pctCeil(f float64) string { return strconv.FormatFloat(f*100, 'f', -1, 64) + "%" }

func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

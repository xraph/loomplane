package manifest

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

const releaseSketch = `
environments:
  production:
    release:
      strategy: canary
      steps: [5, 25, 50, 100]
      hold: 10m
      thresholds:
        error_rate: 1%
        error_rate_delta: 0.5%
        p95_latency_ratio: 1.3
      min_requests: 200
`

func releaseManifest(release string) string {
	return "version: 1\nenvironments:\n  production:\n    release:\n" + release
}

func TestParseReleaseBlock(t *testing.T) {
	m, err := Parse([]byte("version: 1\n" + releaseSketch))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got, ok := m.ReleaseFor("production")
	want := ResolvedRelease{
		Strategy: StrategyCanary, Steps: []int{5, 25, 50, 100}, Hold: 10 * time.Minute,
		ErrorRate: 0.01, ErrorRateDelta: 0.005, P95LatencyRatio: 1.3, MinRequests: 200,
	}
	if !ok || got.Strategy != want.Strategy || !slices.Equal(got.Steps, want.Steps) || got.Hold != want.Hold ||
		got.ErrorRate != want.ErrorRate || got.ErrorRateDelta != want.ErrorRateDelta ||
		got.P95LatencyRatio != want.P95LatencyRatio || got.MinRequests != want.MinRequests {
		t.Errorf("ReleaseFor = %+v, %v; want %+v", got, ok, want)
	}
}

func TestParseReleaseBlock_version2IsRefused(t *testing.T) {
	_, err := Parse([]byte("version: 2\n" + releaseSketch))
	var ps Problems
	if !errors.As(err, &ps) || len(ps) != 1 {
		t.Fatalf("got %v", err)
	}
	if ps[0].Path != "environments.production.release" || !strings.Contains(ps[0].Message, "aren't supported in version 2") {
		t.Errorf("problem = %+v", ps[0])
	}
	if _, err := Parse([]byte("version: 2\nenvironments:\n  production: {}\n")); err != nil {
		t.Errorf("version 2 without a release block: %v", err)
	}
}

func TestReleaseDefaults(t *testing.T) {
	m, err := Parse([]byte(releaseManifest("      strategy: canary\n")))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := m.ReleaseFor("production")
	if !slices.Equal(got.Steps, DefaultCanarySteps) || got.Hold != DefaultHold ||
		got.ErrorRate != 0.01 || got.ErrorRateDelta != 0.005 || got.P95LatencyRatio != 1.3 || got.MinRequests != 200 {
		t.Errorf("canary defaults = %+v", got)
	}
	got.Steps[0] = 99
	if DefaultCanarySteps[0] != 5 {
		t.Error("ReleaseFor shares DefaultCanarySteps with the caller")
	}

	m, err = Parse([]byte(releaseManifest("      strategy: direct\n")))
	if err != nil {
		t.Fatal(err)
	}
	got, _ = m.ReleaseFor("production")
	if got.Steps != nil || got.Hold != 0 || got.ErrorRate != 0.01 || got.ErrorRateDelta != 0.005 ||
		got.P95LatencyRatio != 1.3 || got.MinRequests != 200 {
		t.Errorf("direct defaults = %+v", got)
	}
}

func TestReleaseForUnknownEnvironment(t *testing.T) {
	m, err := Parse([]byte("version: 1\n" + releaseSketch))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := m.ReleaseFor("staging"); ok || got.Strategy != "" {
		t.Errorf("ReleaseFor(staging) = %+v, %v", got, ok)
	}
	m, err = Parse([]byte("version: 1\nenvironments:\n  staging: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.ReleaseFor("staging"); ok {
		t.Error("an environment without a release block reported one")
	}
}

func TestReleaseValidation(t *testing.T) {
	tests := []struct {
		name, release, path string
	}{
		{"unknown strategy", "      strategy: rolling\n", "environments.production.release.strategy"},
		{"missing strategy", "      min_requests: 5\n", "environments.production.release.strategy"},
		{"repeated step", "      strategy: canary\n      steps: [5, 5, 100]\n", "environments.production.release.steps"},
		{"decreasing step", "      strategy: canary\n      steps: [25, 5, 100]\n", "environments.production.release.steps"},
		{"zero step", "      strategy: canary\n      steps: [0, 100]\n", "environments.production.release.steps"},
		{"step over 100", "      strategy: canary\n      steps: [5, 101]\n", "environments.production.release.steps"},
		{"last step not 100", "      strategy: canary\n      steps: [5, 50]\n", "environments.production.release.steps"},
		{"too many steps", "      strategy: canary\n      steps: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 100]\n", "environments.production.release.steps"},
		{"steps with direct", "      strategy: direct\n      steps: [100]\n", "environments.production.release.steps"},
		{"hold with direct", "      strategy: direct\n      hold: 10m\n", "environments.production.release.hold"},
		{"hold with blue_green", "      strategy: blue_green\n      hold: 10m\n", "environments.production.release.hold"},
		{"hold in seconds", "      strategy: canary\n      hold: 30s\n", "environments.production.release.hold"},
		{"hold over a day", "      strategy: canary\n      hold: 25h\n", "environments.production.release.hold"},
		{"hold with a bad unit", "      strategy: canary\n      hold: 10x\n", "environments.production.release.hold"},
		{"hold not whole minutes", "      strategy: canary\n      hold: 90s\n", "environments.production.release.hold"},
		{"error rate without percent", "      strategy: canary\n      thresholds:\n        error_rate: \"1\"\n", "environments.production.release.thresholds.error_rate"},
		{"error rate zero", "      strategy: canary\n      thresholds:\n        error_rate: 0%\n", "environments.production.release.thresholds.error_rate"},
		{"error rate over 100", "      strategy: canary\n      thresholds:\n        error_rate: 101%\n", "environments.production.release.thresholds.error_rate"},
		{"error rate negative", "      strategy: canary\n      thresholds:\n        error_rate: -1%\n", "environments.production.release.thresholds.error_rate"},
		{"delta zero", "      strategy: canary\n      thresholds:\n        error_rate_delta: 0%\n", "environments.production.release.thresholds.error_rate_delta"},
		{"latency ratio low", "      strategy: canary\n      thresholds:\n        p95_latency_ratio: 0.9\n", "environments.production.release.thresholds.p95_latency_ratio"},
		{"latency ratio high", "      strategy: canary\n      thresholds:\n        p95_latency_ratio: 11\n", "environments.production.release.thresholds.p95_latency_ratio"},
		{"min requests negative", "      strategy: canary\n      min_requests: -1\n", "environments.production.release.min_requests"},
		{"min requests huge", "      strategy: canary\n      min_requests: 1000001\n", "environments.production.release.min_requests"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(releaseManifest(tt.release)))
			var ps Problems
			if !errors.As(err, &ps) || len(ps) != 1 || ps[0].Path != tt.path {
				t.Fatalf("got %v, want one problem at %s", err, tt.path)
			}
		})
	}

	t.Run("steps only a canary has", func(t *testing.T) {
		_, err := Parse([]byte(releaseManifest("      strategy: direct\n      steps: [100]\n")))
		if err == nil || !strings.Contains(err.Error(), "only a canary has") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("environment name", func(t *testing.T) {
		_, err := Parse([]byte("version: 1\nenvironments:\n  Prod:\n    release:\n      strategy: direct\n"))
		var ps Problems
		if !errors.As(err, &ps) || len(ps) != 1 || ps[0].Path != "environments.Prod" {
			t.Errorf("got %v", err)
		}
	})
	t.Run("accepted", func(t *testing.T) {
		for _, release := range []string{
			"      strategy: canary\n      steps: []\n",
			"      strategy: canary\n      hold: 1h30m\n",
			"      strategy: canary\n      hold: 24h\n",
			"      strategy: canary\n      steps: [100]\n",
			"      strategy: blue_green\n      min_requests: 1\n      thresholds:\n        error_rate: 100%\n        p95_latency_ratio: 10\n",
			"      strategy: direct\n      thresholds:\n        error_rate: 0.5%\n",
		} {
			if _, err := Parse([]byte(releaseManifest(release))); err != nil {
				t.Errorf("%q: %v", release, err)
			}
		}
	})
}

func TestParsePercent(t *testing.T) {
	for in, want := range map[string]float64{"1%": 0.01, "0.5%": 0.005, "100%": 1} {
		got, err := ParsePercent(in)
		if err != nil || got != want {
			t.Errorf("ParsePercent(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "1", "%", "1.%", " 1%", "1 %", "1e2%", "-1%", "+1%", "1%%"} {
		if _, err := ParsePercent(in); err == nil {
			t.Errorf("ParsePercent(%q) succeeded", in)
		}
	}
}

func TestReleaseRoundTrips(t *testing.T) {
	m, err := Parse([]byte("version: 1\n" + releaseSketch))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "error_rate: 1%\n") {
		t.Errorf("Marshal wrote:\n%s", out)
	}
	again, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse(Marshal): %v\n%s", err, out)
	}
	a, _ := m.ReleaseFor("production")
	b, _ := again.ReleaseFor("production")
	if !slices.Equal(a.Steps, b.Steps) || a.Hold != b.Hold || a.ErrorRate != b.ErrorRate ||
		a.ErrorRateDelta != b.ErrorRateDelta || a.P95LatencyRatio != b.P95LatencyRatio || a.MinRequests != b.MinRequests {
		t.Errorf("round trip changed the release: %+v, %+v", a, b)
	}
	out2, _ := Marshal(again)
	if string(out) != string(out2) {
		t.Errorf("Marshal isn't stable:\n%s\n%s", out, out2)
	}
}

func TestReleaseWatch(t *testing.T) {
	for _, tt := range []struct{ release, watch string }{
		{"      strategy: direct\n      watch: 20m\n", "20m"},
		{"      strategy: canary\n      watch: \"0\"\n", "0"},
		{"      strategy: blue_green\n      watch: 1h30m\n", "1h30m"},
		{"      strategy: canary\n      watch: 24h\n", "24h"},
	} {
		m, err := Parse([]byte(releaseManifest(tt.release)))
		if err != nil {
			t.Fatalf("%q: %v", tt.release, err)
		}
		if got := m.Environments["production"].Release.Watch; got != tt.watch {
			t.Errorf("%q: watch %q, want %q", tt.release, got, tt.watch)
		}
	}
	for _, tt := range []struct{ watch, message string }{
		{"25h", "can be at most 24h"},
		{"soon", "isn't a duration such as 15m or 1h"},
		{"90s", "is whole minutes"},
		{"-5m", "can't be negative"},
	} {
		_, err := Parse([]byte(releaseManifest("      strategy: canary\n      watch: " + tt.watch + "\n")))
		var ps Problems
		if !errors.As(err, &ps) || len(ps) != 1 || ps[0].Path != "environments.production.release.watch" ||
			!strings.Contains(err.Error(), "release.watch: ") || !strings.Contains(ps[0].Message, tt.message) {
			t.Errorf("watch %q: got %v, want one problem at release.watch saying %q", tt.watch, err, tt.message)
		}
	}
}

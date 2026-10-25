package manifest

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Release strategies an environment can declare.
const (
	StrategyDirect    = "direct"
	StrategyBlueGreen = "blue_green"
	StrategyCanary    = "canary"
)

// Limits and defaults for a release block.
const (
	MaxReleaseSteps    = 10
	MinHold            = time.Minute
	MaxHold            = 24 * time.Hour
	DefaultHold        = 10 * time.Minute
	DefaultMinRequests = 200
	MaxMinRequests     = 1_000_000
	MinLatencyRatio    = 1.0
	MaxLatencyRatio    = 10.0

	// Defaults for the thresholds, as fractions and a ratio.
	DefaultErrorRate       = 0.01
	DefaultErrorRateDelta  = 0.005
	DefaultP95LatencyRatio = 1.3
)

// DefaultCanarySteps are the traffic percentages a canary moves through when
// the block doesn't list its own.
var DefaultCanarySteps = []int{5, 25, 50, 100}

var releaseStrategies = []string{StrategyDirect, StrategyBlueGreen, StrategyCanary}

var percentRE = regexp.MustCompile(`^\d+(?:\.\d+)?%$`)

// Environment is what one environment declares about itself.
type Environment struct {
	Release *Release `json:"release,omitempty" yaml:"release,omitempty"`
}

// Release is how an environment's releases go live.
type Release struct {
	Strategy string `json:"strategy" yaml:"strategy"`
	// Steps are the traffic percentages a canary moves through, ending at 100.
	Steps []int `json:"steps,omitempty" yaml:"steps,omitempty"`
	// Hold is how long a canary watches each step, as a Go duration of whole
	// minutes from 1m to 24h.
	Hold        string      `json:"hold,omitempty" yaml:"hold,omitempty"`
	Thresholds  *Thresholds `json:"thresholds,omitempty" yaml:"thresholds,omitempty"`
	MinRequests int         `json:"min_requests,omitempty" yaml:"min_requests,omitempty"`
}

// Thresholds are what health may reach before a release is judged bad.
type Thresholds struct {
	ErrorRate       string  `json:"error_rate,omitempty" yaml:"error_rate,omitempty"`             // "1%"
	ErrorRateDelta  string  `json:"error_rate_delta,omitempty" yaml:"error_rate_delta,omitempty"` // "0.5%"
	P95LatencyRatio float64 `json:"p95_latency_ratio,omitempty" yaml:"p95_latency_ratio,omitempty"`
}

// ResolvedRelease is a release block with every default filled in.
type ResolvedRelease struct {
	Strategy                                   string
	Steps                                      []int
	Hold                                       time.Duration
	ErrorRate, ErrorRateDelta, P95LatencyRatio float64 // fractions and a ratio
	MinRequests                                int
}

// ParsePercent reads a percentage such as "0.5%" as a fraction. It takes
// digits, an optional fractional part and a trailing percent sign, nothing else.
func ParsePercent(s string) (float64, error) {
	if !percentRE.MatchString(s) {
		return 0, fmt.Errorf("%q isn't a percentage such as 1%% or 0.5%%", s)
	}
	f, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
	if err != nil {
		return 0, fmt.Errorf("%q isn't a percentage such as 1%% or 0.5%%", s)
	}
	return f / 100, nil
}

// ReleaseFor is env's release block with defaults filled in. It reports false
// when the environment declares no release block, so a caller can tell
// "declared direct" from "not declared".
func (m *Manifest) ReleaseFor(env string) (ResolvedRelease, bool) {
	e, ok := m.Environments[env]
	if !ok || e.Release == nil {
		return ResolvedRelease{}, false
	}
	r := e.Release
	out := ResolvedRelease{
		Strategy:        r.Strategy,
		ErrorRate:       DefaultErrorRate,
		ErrorRateDelta:  DefaultErrorRateDelta,
		P95LatencyRatio: DefaultP95LatencyRatio,
		MinRequests:     DefaultMinRequests,
	}
	if r.Strategy == StrategyCanary {
		out.Steps = slices.Clone(r.Steps)
		if len(out.Steps) == 0 {
			out.Steps = slices.Clone(DefaultCanarySteps)
		}
		out.Hold = DefaultHold
		if d, err := parseHold(r.Hold); err == nil && r.Hold != "" {
			out.Hold = d
		}
	}
	if r.MinRequests != 0 {
		out.MinRequests = r.MinRequests
	}
	if t := r.Thresholds; t != nil {
		if f, err := ParsePercent(t.ErrorRate); err == nil {
			out.ErrorRate = f
		}
		if f, err := ParsePercent(t.ErrorRateDelta); err == nil {
			out.ErrorRateDelta = f
		}
		if t.P95LatencyRatio != 0 {
			out.P95LatencyRatio = t.P95LatencyRatio
		}
	}
	return out, true
}

// parseHold reads a hold: a Go duration of whole minutes, 1m to 24h.
func parseHold(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	switch {
	case err != nil:
		return 0, fmt.Errorf("%q isn't a duration such as 10m or 1h30m", s)
	case d%time.Minute != 0:
		return 0, fmt.Errorf("%q isn't whole minutes", s)
	case d < MinHold || d > MaxHold:
		return 0, fmt.Errorf("%q is outside 1m to 24h", s)
	}
	return d, nil
}

func (m *Manifest) validateEnvironments(add func(path, format string, args ...any)) {
	for _, env := range sortedKeys(m.Environments) {
		path := "environments." + env
		if !envRE.MatchString(env) {
			add(path, "isn't an environment name")
		}
		r := m.Environments[env].Release
		if r == nil {
			continue
		}
		rpath := path + ".release"
		if m.Version == FederationVersion {
			add(rpath, "release blocks aren't supported in version 2 manifests yet; use version 1")
			continue
		}
		validateRelease(add, rpath, r)
	}
}

func validateRelease(add func(path, format string, args ...any), path string, r *Release) {
	if !slices.Contains(releaseStrategies, r.Strategy) {
		add(path+".strategy", "%q isn't a strategy; use one of %s", r.Strategy, strings.Join(releaseStrategies, ", "))
	}
	canary := r.Strategy == StrategyCanary
	switch {
	case len(r.Steps) > 0 && !canary:
		add(path+".steps", "only a canary has steps")
	case len(r.Steps) > 0:
		checkSteps(add, path+".steps", r.Steps)
	}
	if r.Hold != "" {
		if !canary {
			add(path+".hold", "only a canary has a hold")
		} else if _, err := parseHold(r.Hold); err != nil {
			add(path+".hold", "%v", err)
		}
	}
	if r.MinRequests != 0 && (r.MinRequests < 1 || r.MinRequests > MaxMinRequests) {
		add(path+".min_requests", "is %d; use 1 to %d", r.MinRequests, MaxMinRequests)
	}
	t := r.Thresholds
	if t == nil {
		return
	}
	checkPercent(add, path+".thresholds.error_rate", t.ErrorRate)
	checkPercent(add, path+".thresholds.error_rate_delta", t.ErrorRateDelta)
	if t.P95LatencyRatio != 0 && (t.P95LatencyRatio < MinLatencyRatio || t.P95LatencyRatio > MaxLatencyRatio) {
		add(path+".thresholds.p95_latency_ratio", "is %v; use 1 to 10", t.P95LatencyRatio)
	}
}

func checkSteps(add func(path, format string, args ...any), path string, steps []int) {
	if len(steps) > MaxReleaseSteps {
		add(path, "has %d steps; the most is %d", len(steps), MaxReleaseSteps)
		return
	}
	prev := 0
	for i, s := range steps {
		switch {
		case s < 1 || s > 100:
			add(path, "step %d is %d; each step is 1 to 100", i+1, s)
			return
		case s <= prev:
			add(path, "steps must increase, but step %d (%d) doesn't follow %d", i+1, s, prev)
			return
		}
		prev = s
	}
	if prev != 100 {
		add(path, "the last step must be 100, not %d", prev)
	}
}

func checkPercent(add func(path, format string, args ...any), path, s string) {
	if s == "" {
		return
	}
	f, err := ParsePercent(s)
	switch {
	case err != nil:
		add(path, "%v", err)
	case f <= 0 || f > 1:
		add(path, "%q must be above 0%% and at most 100%%", s)
	}
}

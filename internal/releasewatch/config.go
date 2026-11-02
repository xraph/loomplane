package releasewatch

import (
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/usage"
	"github.com/xraph/loomplane/pkg/manifest"
)

// Settings are how an environment's releases are watched.
type Settings struct {
	Duration time.Duration // 0: no watch
	Strategy string
}

// Config reads an environment's watch from its release block: the block's
// watch when it names one, else DefaultDuration for canary and blue_green
// and none for direct. It reports false when the environment declares no
// release block, which arms no watch. Thresholds aren't read here: a watch
// judges with its plan's.
func Config(m *manifest.Manifest, environment string) (Settings, bool) {
	if m == nil {
		return Settings{}, false
	}
	resolved, ok := m.ReleaseFor(environment)
	if !ok {
		return Settings{}, false
	}
	out := Settings{Strategy: resolved.Strategy}
	if raw := m.Environments[environment].Release.Watch; raw != "" {
		if d, err := manifest.ParseWatch(raw); err == nil {
			out.Duration = d
		}
		return out, true
	}
	if resolved.Strategy == manifest.StrategyCanary || resolved.Strategy == manifest.StrategyBlueGreen {
		out.Duration = DefaultDuration
	}
	return out, true
}

// Watchable reports whether a service on provider can be watched: its
// cloud reports both requests and errors to Loomplane. When it can't, the
// reason starts "not watched: ".
func Watchable(provider string) (ok bool, reason string) {
	if s := releasehealth.SupportFor(provider); s.Requests && s.Errors {
		return true, ""
	}
	if reason := usage.NoTraffic[provider]; reason != "" {
		return false, "not watched: " + reason
	}
	return false, "not watched: Loomplane reads no request and error counts from " + providerTitle(provider) + "."
}

var titles = sync.OnceValue(func() map[string]string {
	out := map[string]string{}
	for _, p := range providers.Catalog() {
		if p.Title != "" {
			out[p.Name] = p.Title
		}
	}
	return out
})

// providerTitle names a built-in provider for people, and any other by its
// name.
func providerTitle(name string) string {
	if t := titles()[name]; t != "" {
		return t
	}
	return name
}

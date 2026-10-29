package releasewatch

import (
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/pkg/manifest"
)

func parse(t *testing.T, release string) *manifest.Manifest {
	t.Helper()
	body := "version: 1\n"
	if release != "" {
		body += "environments:\n  production:\n    release:\n" + release
	}
	m, err := manifest.Parse([]byte(body))
	if err != nil {
		t.Fatalf("parse %q: %v", release, err)
	}
	return m
}

func TestConfig(t *testing.T) {
	for _, tt := range []struct {
		name, release string
		want          time.Duration
		strategy      string
		ok            bool
	}{
		{"no release block", "", 0, "", false},
		{"direct", "      strategy: direct\n", 0, manifest.StrategyDirect, true},
		{"direct with a watch", "      strategy: direct\n      watch: 20m\n", 20 * time.Minute, manifest.StrategyDirect, true},
		{"canary", "      strategy: canary\n", DefaultDuration, manifest.StrategyCanary, true},
		{"blue_green", "      strategy: blue_green\n", DefaultDuration, manifest.StrategyBlueGreen, true},
		{"canary with the watch off", "      strategy: canary\n      watch: \"0\"\n", 0, manifest.StrategyCanary, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Config(parse(t, tt.release), "production")
			if ok != tt.ok || got.Duration != tt.want || got.Strategy != tt.strategy {
				t.Fatalf("Config = %+v, %v; want %v %q %v", got, ok, tt.want, tt.strategy, tt.ok)
			}
		})
	}
	if got, ok := Config(nil, "production"); ok || got.Duration != 0 {
		t.Fatalf("Config(nil) = %+v, %v", got, ok)
	}
	if got, ok := Config(parse(t, "      strategy: canary\n"), "staging"); ok || got.Duration != 0 {
		t.Fatalf("Config(staging) = %+v, %v", got, ok)
	}
	for _, tt := range []struct{ watch, message string }{
		{"25h", "can be at most 24h"},
		{"soon", "isn't a duration"},
		{"90s", "is whole minutes"},
	} {
		_, err := manifest.Parse([]byte("version: 1\nenvironments:\n  production:\n    release:\n      strategy: canary\n      watch: " + tt.watch + "\n"))
		if err == nil || !strings.Contains(err.Error(), "release.watch: ") || !strings.Contains(err.Error(), tt.message) {
			t.Errorf("watch %s: %v, want %q", tt.watch, err, tt.message)
		}
	}
}

func TestWatchable(t *testing.T) {
	for _, p := range []string{"cloud-run", "ecs-fargate", "azure-container-apps", "render"} {
		if ok, reason := Watchable(p); !ok || reason != "" {
			t.Errorf("%s: %v %q, want watchable", p, ok, reason)
		}
	}
	for p, want := range map[string]string{
		"kubernetes":   "not watched: Services on your own clusters report to your own monitoring, which Loomplane doesn't read.",
		"server":       "not watched: Services on your own servers report to your own monitoring, which Loomplane doesn't read.",
		"vercel":       "not watched: Loomplane reads no request and error counts from Vercel.",
		"digitalocean": "not watched: Loomplane reads no request and error counts from DigitalOcean App Platform.",
		"my-plugin":    "not watched: Loomplane reads no request and error counts from my-plugin.",
	} {
		ok, reason := Watchable(p)
		if ok || reason != want {
			t.Errorf("%s: %v %q, want %q", p, ok, reason, want)
		}
		if s := releasehealth.SupportFor(p); s.Requests && s.Errors {
			t.Errorf("%s reports requests and errors, so it should be watchable", p)
		}
	}
}

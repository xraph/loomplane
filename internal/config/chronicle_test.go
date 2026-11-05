package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckpointDefaults(t *testing.T) {
	c, err := FromSource(mapGetter{"database.url": testDSN})
	if err != nil {
		t.Fatal(err)
	}
	want := CheckpointsConfig{Enabled: true, EveryEvents: 1000, EveryInterval: 15 * time.Minute}
	if c.Chronicle.Checkpoints != want {
		t.Fatalf("checkpoints %+v, want %+v", c.Chronicle.Checkpoints, want)
	}
}

func TestCheckpointsFromConfigYAML(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name                   string
		enabled, events, every string
		want                   CheckpointsConfig
	}{
		{"defaults", "", "", "", CheckpointsConfig{Enabled: true, EveryEvents: 1000, EveryInterval: 15 * time.Minute}},
		{"set", "true", "200", "5m", CheckpointsConfig{Enabled: true, EveryEvents: 200, EveryInterval: 5 * time.Minute}},
		{"off", "false", "", "", CheckpointsConfig{EveryEvents: 1000, EveryInterval: 15 * time.Minute}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("LOOMPLANE_DATABASE_URL", testDSN)
			t.Setenv("LOOMPLANE_CHRONICLE_CHECKPOINTS_ENABLED", tt.enabled)
			t.Setenv("LOOMPLANE_CHRONICLE_CHECKPOINTS_EVERY_EVENTS", tt.events)
			t.Setenv("LOOMPLANE_CHRONICLE_CHECKPOINTS_EVERY_INTERVAL", tt.every)
			c, err := Load(dir)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.Chronicle.Checkpoints != tt.want {
				t.Fatalf("checkpoints %+v, want %+v", c.Chronicle.Checkpoints, tt.want)
			}
		})
	}
}

func TestCheckpointsRejectsBadSettings(t *testing.T) {
	for _, tt := range []struct {
		name string
		src  mapGetter
		want string
	}{
		{"negative events", mapGetter{"chronicle.checkpoints.every_events": "-1"}, "every_events"},
		{"short interval", mapGetter{"chronicle.checkpoints.every_interval": "500ms"}, "every_interval"},
		{"not a duration", mapGetter{"chronicle.checkpoints.every_interval": "often"}, "every_interval"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.src["database.url"] = testDSN
			_, err := FromSource(tt.src)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("FromSource = %v, want an error naming %s", err, tt.want)
			}
		})
	}
	// Off, the numbers aren't used, so they aren't judged.
	if _, err := FromSource(mapGetter{"database.url": testDSN, "chronicle.checkpoints.enabled": "false", "chronicle.checkpoints.every_events": "-1"}); err != nil {
		t.Fatalf("off with a bad count: %v", err)
	}
}

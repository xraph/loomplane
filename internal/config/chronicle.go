package config

import (
	"errors"
	"fmt"
	"time"
)

// Checkpoint defaults: a busy stream is signed every thousand events, a
// quiet one at least every fifteen minutes.
const (
	DefaultCheckpointEveryEvents   = 1000
	DefaultCheckpointEveryInterval = 15 * time.Minute
)

// ChronicleConfig is the audit trail's own settings.
type ChronicleConfig struct {
	Checkpoints CheckpointsConfig
}

// CheckpointsConfig is how often the audit trail's streams are signed.
// The signing key lives in Vault; its public half is logged at start.
type CheckpointsConfig struct {
	// Enabled turns signing on. Every API replica signs; replicas that
	// race for the same checkpoint store it once.
	Enabled bool
	// EveryEvents signs a stream once it has gained this many events. 0
	// leaves only EveryInterval.
	EveryEvents int
	// EveryInterval signs a stream that has gained any event at least
	// this often.
	EveryInterval time.Duration
}

// Validate refuses counts below zero and intervals under a second.
func (c CheckpointsConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	var errs []error
	if c.EveryEvents < 0 {
		errs = append(errs, fmt.Errorf("chronicle.checkpoints.every_events can't be negative, got %d", c.EveryEvents))
	}
	if c.EveryInterval < time.Second {
		errs = append(errs, fmt.Errorf("chronicle.checkpoints.every_interval must be at least 1s, got %s", c.EveryInterval))
	}
	return errors.Join(errs...)
}

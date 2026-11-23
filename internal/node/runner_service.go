package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	runnerUnitName = "loomplane-runner.service"
	// runnerBinDir is root's copy of the binary the runner service runs.
	runnerBinDir = "/usr/local/lib/loomplane-runner"
	// DefaultRunnerStateDir is where a runner keeps its identity and config.
	DefaultRunnerStateDir = "/var/lib/loomplane-runner"
	// DefaultRunnerFirecrackerConfig is the Firecracker host config a runner
	// builds with, in the host agent's format.
	DefaultRunnerFirecrackerConfig = "/etc/loomplane-runner/firecracker.json"
)

// RunnerService installs a runner as a systemd unit and controls it. A
// runner's unit runs as root, unlike a node's.
type RunnerService interface {
	// Install copies the binary and writes the unit. It doesn't start it.
	Install(ctx context.Context, binary, stateDir string) error
	// Start enables the unit, so it comes back after a reboot, and starts it.
	Start(ctx context.Context) error
	// Stop disables and stops the unit, so it stays stopped across reboots
	// until Start runs again.
	Stop(ctx context.Context) error
	// Remove stops the unit and deletes it.
	Remove(ctx context.Context) error
	// State is whether the unit runs, and whether it starts at boot.
	State(ctx context.Context) (active, enabled bool)
}

type systemdRunner struct {
	run     Runner
	unitDir string
	binDir  string
}

func newSystemdRunner(run Runner) *systemdRunner {
	return &systemdRunner{run: run, unitDir: systemdUnitDir, binDir: runnerBinDir}
}

func (m *systemdRunner) Install(ctx context.Context, binary, stateDir string) error {
	u := Unit{Binary: binary, StateDir: stateDir, BinDir: m.binDir}
	if err := checkUnit(u, true); err != nil {
		return err
	}
	unit, err := render("templates/loomplane-runner.service", map[string]string{"StateDir": stateDir, "Exec": u.exec()})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	if err := installBinary(binary, m.binDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(m.unitDir, 0o755); err != nil { //nolint:gosec // systemd reads the unit dir as any user
		return err
	}
	if err := os.WriteFile(filepath.Join(m.unitDir, runnerUnitName), []byte(unit), 0o644); err != nil { //nolint:gosec // a unit file is world readable
		return err
	}
	return m.systemctl(ctx, "daemon-reload")
}

func (m *systemdRunner) unit() string { return strings.TrimSuffix(runnerUnitName, ".service") }

func (m *systemdRunner) Start(ctx context.Context) error {
	return m.systemctl(ctx, "enable", "--now", m.unit())
}

func (m *systemdRunner) Stop(ctx context.Context) error {
	return m.systemctl(ctx, "disable", "--now", m.unit())
}

func (m *systemdRunner) Remove(ctx context.Context) error {
	_ = m.systemctl(ctx, "disable", "--now", m.unit()) //nolint:errcheck // a unit never loaded fails to disable; the file is what matters
	if err := os.Remove(filepath.Join(m.unitDir, runnerUnitName)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return m.systemctl(ctx, "daemon-reload")
}

func (m *systemdRunner) State(ctx context.Context) (active, enabled bool) {
	_, aerr := m.run.Run(ctx, "systemctl", "is-active", "--quiet", m.unit())
	_, eerr := m.run.Run(ctx, "systemctl", "is-enabled", "--quiet", m.unit())
	return aerr == nil, eerr == nil
}

func (m *systemdRunner) systemctl(ctx context.Context, args ...string) error {
	if _, err := m.run.Run(ctx, "systemctl", args...); err != nil {
		return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

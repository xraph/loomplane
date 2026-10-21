package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/agent"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
)

func qualifyAgentExecutable(ctx context.Context, enrollments *workloadidentity.EnrollmentStore, issuer workloadidentity.EnrollmentIssuer, binding workloadidentity.Binding, endpoint, socket string, signer *access.SnapshotSigner, policies *access.PolicyStore) (err error) {
	executable, err := os.ReadFile("/loomplane-agent")
	if err != nil {
		return err
	}
	digest := sha256.Sum256(executable)
	binding.Executable = "/loomplane-agent"
	binding.SHA256 = hex.EncodeToString(digest[:])
	binding.CheckedAt = time.Now()
	record, err := enrollments.Reserve(ctx, binding, map[string]runtimecatalog.ExportBounds{})
	if err != nil {
		return err
	}
	if stepErr := enrollments.Issue(ctx, binding.Destination.OrgID, record.ID, issuer); stepErr != nil {
		return stepErr
	}
	directory, err := os.MkdirTemp("", "loomplane-agent-executable-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(directory)) }()
	cfg := agent.LaunchConfig{Version: 1, ControlEndpoint: endpoint, WorkloadAPI: socket, StatePath: filepath.Join(directory, "state", "agent.db"), EnrollmentID: record.ID, EnrollmentExpiresAt: record.ExpiresAt, Registration: record.Registration, Namespace: "fixture", TrustGeneration: 1, SigningKeys: []access.SnapshotKey{signer.Key()}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	file := filepath.Join(directory, "agent.json")
	if stepErr := os.WriteFile(file, raw, 0o600); stepErr != nil {
		return stepErr
	}
	var output bytes.Buffer
	command := exec.CommandContext(ctx, "/loomplane-agent", "--config", file) // #nosec G204 -- Fixed executable with this fixture's owned config path, without a shell.
	command.Stdout, command.Stderr = &output, &output
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	command.WaitDelay = 3 * time.Second
	if stepErr := command.Start(); stepErr != nil {
		return stepErr
	}
	finished := make(chan error, 1)
	go func() { finished <- command.Wait() }()
	waited := false
	defer func() {
		if !waited {
			if signalErr := command.Process.Signal(syscall.SIGTERM); signalErr != nil && !errors.Is(signalErr, os.ErrProcessDone) {
				err = errors.Join(err, signalErr)
			}
			select {
			case <-finished:
			case <-time.After(3 * time.Second):
				if killErr := command.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
					err = errors.Join(err, killErr)
				}
				<-finished
			}
		}
	}()
	var leaseUntil time.Time
	ready, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for leaseUntil.IsZero() {
		select {
		case exitErr := <-finished:
			waited = true
			return fmt.Errorf("agent executable stopped before acknowledgment: %w (%s)", exitErr, output.String())
		case <-ready.Done():
			return ready.Err()
		case <-ticker.C:
			receipt, readErr := policies.Delivery(ready, record.Binding.Agent)
			if readErr == nil && receipt.AcknowledgedSequence > 0 {
				leaseUntil = receipt.LeaseUntil
			}
		}
	}
	if stepErr := command.Process.Signal(syscall.SIGTERM); stepErr != nil {
		return stepErr
	}
	select {
	case exitErr := <-finished:
		waited = true
		if exitErr != nil {
			return fmt.Errorf("agent graceful shutdown: %w (%s)", exitErr, output.String())
		}
	case <-time.After(3 * time.Second):
		return errors.New("agent did not stop on SIGTERM")
	}
	replayCtx, stopReplay := context.WithTimeout(ctx, 10*time.Second)
	defer stopReplay()
	replay := exec.CommandContext(replayCtx, "/loomplane-agent", "--config", file) // #nosec G204 -- Reuses the same fixed executable and owned config path.
	if replayErr := replay.Run(); replayErr == nil || replayCtx.Err() != nil {
		return errors.New("agent executable did not reject consumed enrollment")
	}
	restored, err := agent.OpenStore(cfg.StatePath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, restored.Close()) }()
	if _, stepErr := restored.NextSequence(ctx); !errors.Is(stepErr, agent.ErrClosed) {
		return errors.New("restarted executable retained local authority")
	}
	if stepErr := restored.Activate(record.Binding.Agent); !errors.Is(stepErr, agent.ErrClosed) {
		return errors.New("agent executable did not persist its enrollment epoch")
	}
	if stepErr := until(ctx, func() bool { return !leaseUntil.After(time.Now()) }); stepErr != nil {
		return stepErr
	}
	if stepErr := enrollments.Revoke(ctx, binding.Destination.OrgID, record.ID, issuer); stepErr != nil {
		return stepErr
	}
	fmt.Println("PASS agent executable attestation, configured enrollment, signed acknowledgment, graceful shutdown and restart quarantine")
	return nil
}

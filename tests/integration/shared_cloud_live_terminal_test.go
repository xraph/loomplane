//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
)

type liveTerminalHost struct {
	Index                 int      `json:"index"`
	ReadCode              string   `json:"read_code"`
	OperationStatus       string   `json:"operation_status,omitempty"`
	OperationCode         string   `json:"operation_code,omitempty"`
	Enrolled              bool     `json:"enrolled"`
	Attempt               int      `json:"attempt"`
	Attempted             bool     `json:"attempted"`
	ProviderActions       []string `json:"provider_actions"`
	ActionsTruncated      bool     `json:"actions_truncated"`
	ServerStatus          string   `json:"server_status,omitempty"`
	RuntimePresent        bool     `json:"runtime_present"`
	DockerIdentityPresent bool     `json:"docker_identity_present"`
	CPUs                  int      `json:"cpus"`
	MemoryBytes           int64    `json:"memory_bytes"`
	EffectiveCPUMillis    int64    `json:"effective_cpu_millis"`
	EffectiveMemoryBytes  int64    `json:"effective_memory_bytes"`
}

type liveTerminalSnapshot struct {
	Stage      string             `json:"stage"`
	HTTPStatus int                `json:"http_status,omitempty"`
	Code       string             `json:"code"`
	At         time.Time          `json:"at"`
	Hosts      []liveTerminalHost `json:"hosts"`
}

func liveTerminalStatus(value string) string {
	switch value {
	case "cleanup_pending", "routing_pending", "planning", "approved", "blocked", "stale", "canceled", "paused", "reverting", "reverted", "preview", "pending", "running", "waiting", "succeeded", "failed", "ready", "creating", "provisioning", "deleting", "deleted", "disconnected", "in-progress", "completed", "errored":
		return value
	default:
		return "unknown"
	}
}

// Read only the isolated store's accepted operation IDs. This runs before the
// app stops and its database is dropped; it makes no provider requests.
func liveCaptureTerminal(path string, progress *liveProgress, store cloudinfra.Store, org, cloud string, intents []liveHostIntent) error {
	snapshot := liveTerminalSnapshot{Stage: progress.Stage, HTTPStatus: progress.HTTPStatus, Code: "captured", At: time.Now().UTC(), Hosts: []liveTerminalHost{}}
	if !liveStageAllowed(snapshot.Stage) || (snapshot.HTTPStatus != 0 && (snapshot.HTTPStatus < 100 || snapshot.HTTPStatus > 599)) || len(intents) > 2 {
		return fmt.Errorf("invalid terminal diagnostic scope")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for index, intent := range intents {
		host := liveTerminalHost{Index: index, ReadCode: "operation_unavailable", ProviderActions: []string{}}
		operation, err := store.GetOperation(ctx, org, cloud, intent.Operation)
		if err == nil && operation != nil {
			host.OperationStatus = liveTerminalStatus(operation.Status)
			host.OperationCode = liveOperationCode(operation.Message)
			host.Enrolled, host.Attempt, host.Attempted = operation.Enrolled, max(0, operation.Attempt), operation.Attempted
			host.ActionsTruncated = len(operation.Actions) > 8
			for _, action := range operation.Actions[:min(len(operation.Actions), 8)] {
				host.ProviderActions = append(host.ProviderActions, liveTerminalStatus(action.Status))
			}
			host.ReadCode = "server_unavailable"
			server, readErr := store.GetServer(ctx, org, cloud, operation.ServerID)
			if readErr == nil && server != nil {
				host.ReadCode = "captured"
				host.ServerStatus = liveTerminalStatus(server.Status)
				host.RuntimePresent = server.RuntimeConnectionID != ""
				host.DockerIdentityPresent = server.Health.DockerID != ""
				host.CPUs = max(0, server.Health.CPUs)
				host.MemoryBytes = max(0, server.Health.MemoryBytes)
				host.EffectiveCPUMillis = max(0, server.Health.EffectiveCPUMillis)
				host.EffectiveMemoryBytes = max(0, server.Health.EffectiveMemoryBytes)
			}
		}
		if host.ReadCode != "captured" {
			snapshot.Code = "read_incomplete"
		}
		snapshot.Hosts = append(snapshot.Hosts, host)
	}
	file := filepath.Join(filepath.Dir(path), "run-terminal.json")
	reservation, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("terminal diagnostics already exist or cannot be created")
	}
	if err = reservation.Close(); err != nil {
		return fmt.Errorf("close terminal diagnostics failed")
	}
	return liveWrite(file, snapshot)
}

func liveOperationCode(message string) string {
	switch message {
	case "":
		return "none"
	case "The provider outcome is unresolved. Retry reconciles the existing intent without creating a replacement.":
		return "provider_outcome_unresolved"
	case "The reviewed connection or server changed. Resolve the recorded operation before continuing.":
		return "review_changed"
	case "Organization management access was revoked.":
		return "manager_revoked"
	case "Infrastructure verification failed. Check the connection, provider scope and pinned server runtime before retrying.":
		return "verification_failed"
	}
	if strings.HasPrefix(message, cloudinfra.CapacityBlockedPrefix) {
		return "capacity_blocked"
	}
	return "unknown"
}

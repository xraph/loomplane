// Package realtime stores the authoritative event sequence and build output.
package realtime

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	MaxLineBytes    = 16 << 10
	MaxAttemptBytes = 10 << 20
	MaxReplayEvents = 1000
	Retention       = 7 * 24 * time.Hour
)

var ErrInvalid = errors.New("invalid realtime cursor or record")

// Event carries a named payload; cursor/routing fields stay outside the entity.
type Event struct {
	ID        string          `json:"id"`
	OrgID     string          `json:"org_id"`
	ProjectID string          `json:"project_id,omitempty"`
	Type      string          `json:"event"`
	EntityID  string          `json:"entity_id"`
	Data      json.RawMessage `json:"data"`
	Time      time.Time       `json:"time"`
}

// LogInput is one collector line, already scoped to its durable activity.
type LogInput struct {
	OrgID, ProjectID, DeploymentID, ActionID, Attempt, OperationID, ProviderID, Stream, Source, Level, Message string
	Time                                                                                                       time.Time
}

// Log is immutable output retained for replay and export.
type Log struct {
	ID           string    `json:"id" forge:"id"`
	OrgID        string    `json:"org_id"`
	ProjectID    string    `json:"project_id"`
	DeploymentID string    `json:"deployment_id"`
	ActionID     string    `json:"action_id"`
	Attempt      string    `json:"attempt"`
	OperationID  string    `json:"operation_id"`
	ProviderID   string    `json:"provider_id"`
	Stream       string    `json:"stream"`
	Source       string    `json:"source"`
	Level        string    `json:"level"`
	Message      string    `json:"message"`
	Time         time.Time `json:"time"`
	Truncated    bool      `json:"truncated"`
}

type LogPage struct {
	Logs []Log  `json:"logs"`
	Next string `json:"next"`
	Gap  bool   `json:"gap"`
}

func cursor(org string, sequence int64) string { return org + ":" + strconv.FormatInt(sequence, 10) }
func position(org, id string) (int64, error) {
	owner, value, ok := strings.Cut(id, ":")
	if !ok || owner != org || org == "" {
		return 0, ErrInvalid
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 {
		return 0, ErrInvalid
	}
	return n, nil
}

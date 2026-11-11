package fleet

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// Event is the public Go alias for the Fleet event envelope.
type Event = FleetEvent

// FleetEvent contains one versioned public payload. Its distinct type name keeps
// the OpenAPI schema separate from agent activity events.
//
//nolint:revive // Keep the API schema name distinct; Go callers retain the short alias.
type FleetEvent struct {
	OrgID         string          `json:"org_id"`
	Version       int             `json:"version"`
	ID            string          `json:"id"`
	Sequence      int64           `json:"sequence"`
	FleetID       string          `json:"fleet_id"`
	InstanceID    string          `json:"instance_id"`
	EnvironmentID string          `json:"environment_id"`
	OperationID   string          `json:"operation_id,omitempty"`
	ReleaseID     string          `json:"release_id,omitempty"`
	Component     string          `json:"component,omitempty"`
	Generation    int64           `json:"generation"`
	Kind          string          `json:"kind"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Lifecycle     *LifecycleEvent `json:"lifecycle,omitempty"`
	Scaling       *ScalingEvent   `json:"scaling,omitempty"`
	Release       *ReleaseEvent   `json:"release,omitempty"`
}

// LifecycleEvent records a durable operation state using public reason codes.
type LifecycleEvent struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// ScalingEvent links a scaling decision to the instance and Cloud causing demand.
type ScalingEvent struct {
	Service          string    `json:"service"`
	PolicyRevision   int64     `json:"policy_revision"`
	PreviousReplicas int       `json:"previous_replicas"`
	DesiredReplicas  int       `json:"desired_replicas"`
	Reason           string    `json:"reason"`
	Writer           string    `json:"writer"`
	ObservedAt       time.Time `json:"observed_at"`
	CloudID          string    `json:"cloud_id,omitempty"`
}

// ReleaseEvent records one instance's progress through a frozen campaign.
type ReleaseEvent struct {
	TargetGeneration  int64  `json:"target_generation,omitempty"`
	TargetOperationID string `json:"target_operation_id,omitempty"`
	CampaignID        string `json:"campaign_id"`
	Wave              int    `json:"wave"`
	State             string `json:"state"`
	Reason            string `json:"reason,omitempty"`
}

// EventPage uses an instance-bound sequence cursor for polling and SSE replay.
type EventPage struct {
	Acceptance      []EventAcceptance `json:"acceptance"`
	ReplayTruncated bool              `json:"replay_truncated"`
	Items           []Event           `json:"items"`
	NextCursor      string            `json:"next_cursor,omitempty"`
}

// EventAcceptance is the journal outbox status. Accepted means Relay owns delivery.
type EventAcceptance struct {
	EventID   string `json:"event_id"`
	State     string `json:"state"`
	Attempts  int    `json:"attempts"`
	ErrorCode string `json:"error_code,omitempty"`
}

// CallbackQuery selects scoped callback history, including older failed deliveries.
type CallbackQuery struct {
	After      string `json:"after,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	State      string `json:"state,omitempty"`
	EndpointID string `json:"endpoint_id,omitempty"`
}

// CallbackDelivery links a receiver attempt to its stable source event and instance.
type CallbackDelivery struct {
	ID            string     `json:"id"`
	EventID       string     `json:"event_id"`
	InstanceID    string     `json:"instance_id"`
	Generation    int64      `json:"generation"`
	EndpointID    string     `json:"endpoint_id"`
	State         string     `json:"state"`
	Attempts      int        `json:"attempts"`
	CreatedAt     time.Time  `json:"created_at"`
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

// CallbackPage retains the underlying bounded scan's completeness indicator.
type CallbackPage struct {
	Items      []CallbackDelivery `json:"items"`
	NextCursor string             `json:"next_cursor,omitempty"`
	Complete   bool               `json:"complete"`
}

// UnmarshalJSON accepts Relay's exact integer exponent notation without rounding
// event generations or sequences through floating-point values.
func (e *FleetEvent) UnmarshalJSON(data []byte) error {
	if len(data) > 1<<20 {
		return errEventJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return errEventJSON
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errEventJSON
	}
	normalized, err := eventJSONIntegers(value, 0)
	if err != nil {
		return err
	}
	plain, err := json.Marshal(normalized)
	if err != nil {
		return errEventJSON
	}
	type wireEvent Event
	var decoded wireEvent
	if err = json.Unmarshal(plain, &decoded); err != nil {
		return errEventJSON
	}
	*e = Event(decoded)
	return nil
}

var errEventJSON = errors.New("invalid fleet event JSON")

func eventJSONIntegers(value any, depth int) (any, error) {
	if depth > 16 {
		return nil, errEventJSON
	}
	switch v := value.(type) {
	case json.Number:
		raw := v.String()
		if len(raw) > 64 {
			return nil, errEventJSON
		}
		if index := strings.IndexAny(raw, "eE"); index >= 0 {
			exponent, err := strconv.Atoi(raw[index+1:])
			if err != nil || exponent < -64 || exponent > 64 {
				return nil, errEventJSON
			}
		}
		integer, ok := new(big.Rat).SetString(raw)
		if !ok || !integer.IsInt() || !integer.Num().IsInt64() {
			return nil, errEventJSON
		}
		return json.RawMessage(integer.Num().String()), nil
	case map[string]any:
		for key, item := range v {
			normalized, err := eventJSONIntegers(item, depth+1)
			if err != nil {
				return nil, err
			}
			v[key] = normalized
		}
		return v, nil
	case []any:
		for index, item := range v {
			normalized, err := eventJSONIntegers(item, depth+1)
			if err != nil {
				return nil, err
			}
			v[index] = normalized
		}
		return v, nil
	default:
		return value, nil
	}
}

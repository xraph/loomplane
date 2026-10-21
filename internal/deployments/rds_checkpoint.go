package deployments

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

type rdsState struct {
	Version    int                     `json:"rds_checkpoint"`
	Scope      databaseCheckpointScope `json:"scope"`
	Stage      string                  `json:"stage"`
	Intent     string                  `json:"intent"`
	ResourceID string                  `json:"resource_id,omitempty"`
	Attempted  bool                    `json:"attempted,omitempty"`
	Accepted   bool                    `json:"accepted,omitempty"`
	Snapshot   *aws.DBSnapshot         `json:"snapshot,omitempty"`
	checkpoint func([]byte) error
}

func readRDSState(job *Job, cfg *planning.ResourceConfig, instance, region, physical, mode string) (*rdsState, error) {
	scope, err := newDatabaseCheckpointScope(job, cfg, connections.AWS, "", region, instance, mode)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		return nil, err
	}
	intent := fmt.Sprintf("%x", sha256.Sum256(append([]byte("rds:"), raw...)))
	state := &rdsState{Version: 1, Scope: scope, Stage: "instance", Intent: intent, ResourceID: physical, checkpoint: job.Checkpoint}
	stages := []string{"instance", "ready"}
	if mode == "remove" {
		state.Stage = "remove"
		stages = []string{"remove", "removed"}
	}
	if len(job.Resume) == 0 {
		return state, nil
	}
	var restored rdsState
	if err := decodeDatabaseCheckpoint(job.Resume, &restored); err != nil {
		return nil, err
	}
	state = &restored
	state.checkpoint = job.Checkpoint
	if state.Version != 1 || state.Scope != scope || state.Intent != intent || !slices.Contains(stages, state.Stage) ||
		(physical != "" && state.ResourceID != physical) || (state.Accepted && !state.Attempted) ||
		(state.Stage == "ready" && state.ResourceID == "") || (mode == "apply" && (state.Snapshot != nil || state.Accepted)) {
		return nil, errors.New("RDS checkpoint no longer matches its original account, resource and physical identity")
	}
	if state.Stage == "removed" && (state.ResourceID == "" || state.Snapshot == nil || state.Snapshot.ResourceID != state.ResourceID || state.Snapshot.ID != aws.FinalDBSnapshotID(state.ResourceID) || state.Snapshot.InstanceID != instance || state.Snapshot.Status != "available") {
		return nil, errors.New("RDS checkpoint has no verified final snapshot for the removed database")
	}
	return state, nil
}

func (s *rdsState) save() error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return s.checkpoint(raw)
}

func (s *rdsState) capture(physical string) error {
	if physical == "" || (s.ResourceID != "" && s.ResourceID != physical) {
		return errors.New("RDS physical identity changed during checkpoint capture")
	}
	s.ResourceID = physical
	return s.save()
}

func (s *rdsState) createTrack() aws.DBMutation {
	return aws.DBMutation{Attempted: s.Attempted, Before: func() error { s.Attempted = true; return s.save() }, Captured: s.capture}
}

func (s *rdsState) removeTrack() aws.DBRemoval {
	return aws.DBRemoval{Attempted: s.Attempted, Before: func() error { s.Attempted = true; return s.save() }, Accepted: func() error { s.Accepted = true; return s.save() }}
}

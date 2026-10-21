package deployments

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

type cloudSQLState struct {
	Version    int                     `json:"cloud_sql_checkpoint"`
	Scope      databaseCheckpointScope `json:"scope"`
	Stage      string                  `json:"stage"`
	Operation  string                  `json:"operation,omitempty"`
	Uncertain  bool                    `json:"uncertain,omitempty"`
	checkpoint func([]byte) error
}

func readCloudSQLState(job *Job, cfg *planning.ResourceConfig, project, region, instance, mode string) (*cloudSQLState, error) {
	scope, err := newDatabaseCheckpointScope(job, cfg, connections.GCP, project, region, instance, mode)
	if err != nil {
		return nil, err
	}
	state := &cloudSQLState{Version: 1, Scope: scope, Stage: "instance", checkpoint: job.Checkpoint}
	stages := []string{"instance", "database", "user", "ready"}
	if mode == "remove" {
		state.Stage = "remove"
		stages = []string{"remove", "removed"}
	}
	if len(job.Resume) == 0 {
		return state, nil
	}
	var restored cloudSQLState
	if err := decodeDatabaseCheckpoint(job.Resume, &restored); err != nil {
		return nil, err
	}
	state = &restored
	state.checkpoint = job.Checkpoint
	if state.Version != 1 || state.Scope != scope || !slices.Contains(stages, state.Stage) ||
		(state.Operation != "" && !state.Uncertain) || ((state.Stage == "ready" || state.Stage == "removed") && state.Uncertain) {
		return nil, errors.New("cloud sql checkpoint does not match the original account, resource and operation")
	}
	if state.Uncertain && state.Operation == "" {
		return nil, errors.New("cloud sql checkpoint has an uncertain request without a captured operation; reconcile the original cloud operation before retrying")
	}
	return state, nil
}

func (s *cloudSQLState) save() error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return s.checkpoint(raw)
}

func (s *cloudSQLState) track() gcp.SQLMutation {
	return gcp.SQLMutation{Resume: s.Operation,
		Before:  func() error { s.Uncertain = true; return s.save() },
		Started: func(op string) error { s.Operation = op; return s.save() },
	}
}

func (s *cloudSQLState) advance(stage string) error {
	s.Stage, s.Operation, s.Uncertain = stage, "", false
	return s.save()
}

// boundSQLInstance validates the project and region in a native connection name.
func boundSQLInstance(external, project, region string) (instance, actualRegion string, err error) {
	prefix := project + ":"
	if !strings.HasPrefix(external, prefix) {
		return "", "", errors.New("cloud sql binding belongs to a different project")
	}
	actual, instance, ok := strings.Cut(strings.TrimPrefix(external, prefix), ":")
	if !ok || actual == "" || instance == "" || strings.ContainsAny(instance, "/:\\?#") || (region != "" && region != actual) {
		return "", "", errors.New("cloud sql binding does not match its region and instance")
	}
	return instance, actual, nil
}

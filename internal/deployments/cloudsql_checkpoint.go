package deployments

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
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
	ResourceID string                  `json:"resource_id,omitempty"`
	Intent     string                  `json:"intent"`
	Uncertain  bool                    `json:"uncertain,omitempty"`
	checkpoint func([]byte) error
}

func readCloudSQLState(job *Job, cfg *planning.ResourceConfig, project, region, instance, mode string) (*cloudSQLState, error) {
	scope, err := newDatabaseCheckpointScope(job, cfg, connections.GCP, project, region, instance, mode)
	if err != nil {
		return nil, err
	}
	state := &cloudSQLState{Version: 2, Scope: scope, Stage: "instance", checkpoint: job.Checkpoint}
	raw, err := json.Marshal(scope)
	if err != nil {
		return nil, err
	}
	state.Intent = fmt.Sprintf("%x", sha256.Sum256(raw))[:60]
	if job.Binding != nil {
		state.ResourceID = job.Binding.PhysicalID
	}
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
	if restored.Version == 1 {
		return nil, errors.New("cloud sql checkpoint v1 has no incarnation pin; reconcile its original operation before retrying")
	}
	if restored.Intent != state.Intent || (state.ResourceID != "" && restored.ResourceID != state.ResourceID) {
		return nil, errors.New("cloud sql checkpoint incarnation or create intent changed")
	}
	state = &restored
	state.checkpoint = job.Checkpoint
	if state.Version != 2 || state.Scope != scope || !slices.Contains(stages, state.Stage) ||
		(state.Operation != "" && !state.Uncertain) || ((state.Stage == "ready" || state.Stage == "removed") && state.Uncertain) {
		return nil, errors.New("cloud sql checkpoint does not match the original account, resource and operation")
	}
	if state.Stage != "instance" && state.Scope.Mode == "apply" && state.ResourceID == "" {
		return nil, errors.New("cloud sql checkpoint has no original incarnation pin")
	}
	if state.Scope.Mode == "remove" && (state.Operation != "" || state.Stage == "removed") && state.ResourceID == "" {
		return nil, errors.New("cloud sql checkpoint has no original incarnation pin")
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

// inspect saves the first visible incarnation before any subsequent mutation.
func (s *cloudSQLState) inspect(in *gcp.SQLInstance, fresh bool) error {
	pin, err := in.Incarnation(s.Scope.Project, s.Scope.Region, s.Scope.Instance)
	if err != nil {
		return err
	}
	if s.ResourceID != "" {
		if pin != s.ResourceID {
			return errors.New("cloud sql incarnation changed; the original name now belongs to another instance")
		}
		return nil
	}
	if fresh && in.Settings.UserLabels[gcp.SQLIntentLabel] != s.Intent {
		return errors.New("cloud sql incarnation does not carry the original create intent")
	}
	s.ResourceID = pin
	return s.save()
}

func (s *cloudSQLState) guarded(client *gcp.Session, fresh bool) gcp.SQLMutation {
	track := s.track()
	track.Check = func(ctx context.Context) error {
		in, err := client.GetSQLInstance(ctx, s.Scope.Instance)
		if gcp.IsNotFound(err) {
			if s.Stage == "instance" && fresh && s.ResourceID == "" && s.Operation == "" {
				return nil
			}
			if s.Scope.Mode == "remove" && s.Operation != "" {
				return nil
			}
			return errors.New("cloud sql incarnation is missing; reconcile the original instance before retrying")
		}
		if err != nil {
			return err
		}
		return s.inspect(in, fresh)
	}
	return track
}

package deployments

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

type cloudSQLScope struct {
	Mode       string `json:"mode"`
	Deployment string `json:"deployment"`
	Action     string `json:"action"`
	Connection string `json:"connection"`
	Account    string `json:"account"`
	Project    string `json:"project"`
	Region     string `json:"region"`
	Instance   string `json:"instance"`
	Resource   string `json:"resource"`
}

type cloudSQLState struct {
	Version    int           `json:"cloud_sql_checkpoint"`
	Scope      cloudSQLScope `json:"scope"`
	Stage      string        `json:"stage"`
	Operation  string        `json:"operation,omitempty"`
	Uncertain  bool          `json:"uncertain,omitempty"`
	checkpoint func([]byte) error
}

func readCloudSQLState(job *Job, cfg *planning.ResourceConfig, project, region, instance, mode string) (*cloudSQLState, error) {
	if job.Connection == nil || job.Connection.Cloud != connections.GCP || job.Connection.Status != connections.StatusReady ||
		job.Connection.Details["project_id"] != project || job.Connection.ID == "" || job.Deployment == nil || job.Deployment.ID == "" || job.Action.ID == "" ||
		project == "" || region == "" || instance == "" || strings.ContainsAny(instance, "/:\\?#") || job.Checkpoint == nil {
		return nil, errors.New("cloud sql checkpoint requires the original deployment, checked account and resource scope")
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("cloud sql checkpoint resource: %w", err)
	}
	scope := cloudSQLScope{Mode: mode, Deployment: job.Deployment.ID, Action: job.Action.ID, Connection: job.Connection.ID,
		Account: connections.AccountIdentity(job.Connection.Cloud, job.Connection.Account, job.Connection.Details), Project: project, Region: region, Instance: instance, Resource: fmt.Sprintf("%x", sha256.Sum256(raw))}
	state := &cloudSQLState{Version: 1, Scope: scope, Stage: "instance", checkpoint: job.Checkpoint}
	stages := []string{"instance", "database", "user", "ready"}
	if mode == "remove" {
		state.Stage = "remove"
		stages = []string{"remove", "removed"}
	}
	if len(job.Resume) == 0 {
		return state, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(job.Resume))
	decoder.DisallowUnknownFields()
	var restored cloudSQLState
	if err := decoder.Decode(&restored); err != nil {
		return nil, fmt.Errorf("read cloud sql checkpoint: %w", err)
	}
	state = &restored
	state.checkpoint = job.Checkpoint
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid cloud sql checkpoint suffix")
	}
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

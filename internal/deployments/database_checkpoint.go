package deployments

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

// databaseCheckpointScope keeps requested placement separate from the native
// identity captured by each provider. Field names preserve Cloud SQL v1 records.
type databaseCheckpointScope struct {
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

func newDatabaseCheckpointScope(job *Job, cfg *planning.ResourceConfig, cloud, project, region, instance, mode string) (databaseCheckpointScope, error) {
	var scope databaseCheckpointScope
	if job.Connection == nil || job.Connection.Cloud != cloud || job.Connection.Status != connections.StatusReady || job.Connection.ID == "" ||
		job.Deployment == nil || job.Deployment.ID == "" || job.Action.ID == "" || region == "" || instance == "" || strings.ContainsAny(instance, "/:\\?#") || job.Checkpoint == nil || (mode != "apply" && mode != "remove") {
		return scope, errors.New("database checkpoint requires the original deployment, checked account and resource scope")
	}
	if (cloud == connections.GCP && (project == "" || job.Connection.Details["project_id"] != project)) ||
		(cloud == connections.AWS && (project != "" || job.Connection.Account == "" || job.Connection.Details["region"] != region)) {
		return scope, errors.New("database checkpoint does not match its checked account and region")
	}
	identity := connections.AccountIdentity(cloud, job.Connection.Account, job.Connection.Details)
	if identity == "" {
		return scope, errors.New("database checkpoint has no checked account identity")
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return scope, fmt.Errorf("database checkpoint resource: %w", err)
	}
	return databaseCheckpointScope{Mode: mode, Deployment: job.Deployment.ID, Action: job.Action.ID, Connection: job.Connection.ID, Account: identity, Project: project, Region: region, Instance: instance, Resource: fmt.Sprintf("%x", sha256.Sum256(raw))}, nil
}

// decodeDatabaseCheckpoint is called with a fresh provider-specific value so
// omitted fields cannot inherit current scope or version defaults.
func decodeDatabaseCheckpoint(raw []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("read database checkpoint: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("invalid database checkpoint suffix")
	}
	return nil
}

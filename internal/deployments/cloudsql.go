package deployments

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/clouds/progress"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

const (
	// sqlTier is the machine a Cloud SQL instance runs on: one vCPU and
	// 3.75 GB, the smallest dedicated Enterprise tier.
	sqlTier = "db-custom-1-3840"
	// sqlUser is the user Loomplane creates for the application.
	sqlUser = "app"
	// passwordField is where a database's password is kept, in Vault.
	passwordField = "password"
)

// sqlVersions maps a resource kind and version to Cloud SQL's names.
var sqlVersions = map[string]string{
	"postgres/17": "POSTGRES_17", "postgres/16": "POSTGRES_16", "postgres/15": "POSTGRES_15",
	"mysql/8.4": "MYSQL_8_4", "mysql/8.0": "MYSQL_8_0",
}

// CloudSQL provisions Postgres and MySQL on Google Cloud SQL: an instance,
// a database and a user whose password Loomplane generates and keeps in
// Vault. Services on Cloud Run reach it through the Cloud SQL connector,
// a socket Cloud Run mounts, so the instance needs no authorized networks.
type CloudSQL struct {
	Google *gcp.Client
}

var _ Executor = (*CloudSQL)(nil)

type cloudSQLState struct {
	Operation string `json:"operation,omitempty"`
}

// SQLInstanceName is the Cloud SQL instance a resource is in an
// environment: lower case letters, digits and hyphens.
func SQLInstanceName(job *Job) string {
	return "lp-" + strings.ToLower(job.Action.Name+"-"+job.Deployment.Environment+"-"+idSuffix(job.Deployment.ProjectID))
}

// cloudSQLInstance is the Cloud SQL instance a resource is: the one its
// binding names, when that's in the resource's region, or a new one's
// name, which ends with its region. A region can't change in place, so a
// move creates another instance, and it needs a name of its own while the
// old one still runs. Bound instances keep the names they have, including
// those from before names had a region, so nothing live is renamed.
func cloudSQLInstance(job *Job) string {
	region := job.Action.Resource.Region
	if b := job.Binding; b != nil && (b.Region == "" || b.Region == region) {
		if name := sqlInstanceID(b.ExternalID); name != "" {
			return name
		}
	}
	return SQLInstanceName(job) + "-" + strings.ToLower(region)
}

// sqlInstanceID is the instance a connection name names: the last part of
// "project:region:instance", where a domain-scoped project has a colon
// of its own.
func sqlInstanceID(connection string) string {
	parts := strings.Split(connection, ":")
	if len(parts) < 3 {
		return ""
	}
	return parts[len(parts)-1]
}

// Apply implements Executor.
func (c *CloudSQL) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	cfg := job.Action.Resource
	if cfg == nil {
		return nil, fmt.Errorf("%w: cloud sql provisions databases, not %s", ErrNoExecutor, job.Action.Subject)
	}
	version, ok := sqlVersions[cfg.Kind+"/"+cfg.Version]
	if !ok {
		return nil, fmt.Errorf("cloud sql doesn't offer %s %s", cfg.Kind, cfg.Version)
	}
	key, err := gcp.ParseKey(job.Credential)
	if err != nil {
		return nil, err
	}
	s, err := c.Google.Session(ctx, key)
	if err != nil {
		return nil, err
	}
	var state cloudSQLState
	if len(job.Resume) > 0 {
		_ = json.Unmarshal(job.Resume, &state) //nolint:errcheck // an unreadable checkpoint starts over
	}
	save := func() {
		data, _ := json.Marshal(state) //nolint:errcheck // a plain struct always encodes
		_ = job.Checkpoint(data)       //nolint:errcheck // the next heartbeat retries it
	}
	ctx = progress.With(ctx, func(m string) {
		job.Progress(StepDeploying, m)
		save()
	})

	job.Progress(StepDeploying, "Creating the database instance; this takes several minutes")
	in, err := s.EnsureSQLInstance(ctx, gcp.SQLInstanceSpec{
		Name: cloudSQLInstance(job), DatabaseVersion: version, Region: cfg.Region, Tier: sqlTier,
	}, state.Operation, func(op string) {
		state.Operation = op
		save()
	})
	if err != nil {
		return nil, err
	}
	db := databaseName(job.Action.Name)
	if derr := s.EnsureDatabase(ctx, in.Name, db); derr != nil {
		return nil, fmt.Errorf("create the database: %w", derr)
	}
	password, _, err := keptPassword(ctx, job, rand.Text()+rand.Text())
	if err != nil {
		return nil, err
	}
	if err := s.SetUser(ctx, in.Name, sqlUser, password); err != nil {
		return nil, fmt.Errorf("set the database user: %w", err)
	}
	if err := job.Keep(ctx, ResourceURL, []byte(databaseURL(cfg.Kind, password, db, in.ConnectionName))); err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: in.ConnectionName, Revision: in.DatabaseVersion}, nil
}

// keptPassword is a database user's password: the one kept for this
// database, or fresh, kept before it's used so a retry sets the same one.
// isNew reports which.
func keptPassword(ctx context.Context, job *Job, fresh string) (password string, isNew bool, err error) {
	kept, err := job.Recall(ctx, passwordField)
	if err == nil && len(kept) > 0 {
		return string(kept), false, nil
	}
	if err != nil && !errors.Is(err, ErrNoCredential) {
		return "", false, err
	}
	if err := job.Keep(ctx, passwordField, []byte(fresh)); err != nil {
		return "", false, err
	}
	return fresh, true, nil
}

// databaseName is the resource's name as a database name.
func databaseName(name string) string { return strings.ReplaceAll(name, "-", "_") }

// databaseURL is how a service on Cloud Run reaches the database: through
// the socket the Cloud SQL connector mounts at /cloudsql.
func databaseURL(kind, password, db, connection string) string {
	creds := url.UserPassword(sqlUser, password).String()
	socket := "/cloudsql/" + connection
	if kind == "mysql" {
		return "mysql://" + creds + "@localhost/" + db + "?socket=" + url.QueryEscape(socket)
	}
	return "postgresql://" + creds + "@/" + db + "?host=" + url.QueryEscape(socket)
}

// Remove implements Executor: it deletes the instance, with its data, and
// the credentials Loomplane kept for it.
func (c *CloudSQL) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	key, err := gcp.ParseKey(job.Credential)
	if err != nil {
		return err
	}
	s, err := c.Google.Session(ctx, key)
	if err != nil {
		return err
	}
	_, instance, _ := strings.Cut(strings.TrimPrefix(b.ExternalID, key.ProjectID+":"), ":")
	if instance == "" {
		return fmt.Errorf("the binding names no instance: %q", b.ExternalID)
	}
	if err := s.DeleteSQLInstance(ctx, instance); err != nil {
		return err
	}
	return job.Forget(ctx)
}

// Observe implements Executor.
func (c *CloudSQL) Observe(ctx context.Context, _ *connections.Connection, credential []byte, b *planning.Binding) (*Observation, error) {
	key, err := gcp.ParseKey(credential)
	if err != nil {
		return nil, err
	}
	s, err := c.Google.Session(ctx, key)
	if err != nil {
		return nil, err
	}
	_, instance, _ := strings.Cut(strings.TrimPrefix(b.ExternalID, key.ProjectID+":"), ":")
	in, err := s.GetSQLInstance(ctx, instance)
	if gcp.IsNotFound(err) {
		return &Observation{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &Observation{Exists: true, Revision: in.DatabaseVersion}, nil
}

package deployments

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/clouds/progress"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
)

const (
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
	// PSCConnector replaces transport only for trusted Go integrations. Source,
	// endpoint and SQL checks always run; production uses Google's PSC dialer.
	PSCConnector func(context.Context, *gcp.Session, string, string) (gcp.SQLConnector, error)
}

var _ Executor = (*CloudSQL)(nil)

// RetainCheckpointOnRetry keeps captured operations across explicit retries.
func (*CloudSQL) RetainCheckpointOnRetry() bool { return true }

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
	request, err := managedDatabaseRequest(job, "cloud-sql")
	if err != nil {
		return nil, err
	}
	version, ok := sqlVersions[cfg.Kind+"/"+cfg.Version]
	if !ok {
		return nil, fmt.Errorf("cloud sql doesn't offer %s %s", cfg.Kind, cfg.Version)
	}
	key, err := gcp.ParseKey(job.Credential)
	if err != nil {
		return nil, err
	}
	if cfg.Managed != nil && key.ProjectID != job.Connection.Details["project_id"] {
		return nil, fmt.Errorf("managed database review changed: the credential belongs to a different project")
	}
	instance := cloudSQLInstance(job)
	if job.Binding != nil {
		bound, _, bindErr := boundSQLInstance(job.Binding.ExternalID, key.ProjectID, cfg.Region)
		if bindErr != nil {
			return nil, bindErr
		}
		if bound != instance {
			return nil, errors.New("cloud sql binding no longer matches the saved deployment")
		}
	}
	state, err := readCloudSQLState(job, cfg, key.ProjectID, cfg.Region, instance, "apply")
	if err != nil {
		return nil, err
	}
	var password string
	if job.Binding != nil {
		password, err = originalSQLPassword(ctx, job)
		if err != nil {
			return nil, err
		}
	}
	s, err := c.Google.Session(ctx, key)
	if err != nil {
		return nil, err
	}
	if recoverErr := state.recoverCreate(ctx, s, version); recoverErr != nil {
		return nil, recoverErr
	}
	ctx = progress.With(ctx, func(m string) { job.Progress(StepDeploying, m) })
	job.Progress(StepDeploying, "Creating the database instance; this takes several minutes")
	spec := gcp.SQLInstanceSpec{
		Name: instance, DatabaseVersion: version, Region: cfg.Region, Tier: request.ComputeClass,
		Existing: job.Binding != nil, Intent: state.Intent,
		Inspect: func(in *gcp.SQLInstance) error { return state.inspect(in, job.Binding == nil) },
	}
	if cfg.Managed != nil {
		spec.Storage = &gcp.SQLStorageSettings{SizeGB: request.StorageGB, Type: request.StorageType, RetainedBackups: request.BackupRetentionValue, RecoveryDays: request.PointInTimeRecoveryDays}
	}
	var in *gcp.SQLInstance
	psc := cfg.Managed != nil && cfg.Managed.CloudSQLPSC != (manageddb.CloudSQLSourceEndpoint{})
	if state.Stage == "instance" {
		if psc {
			in, err = s.EnsureSQLPITRSource(ctx, spec, cfg.Managed.CloudSQLPSC, state.guarded(s, job.Binding == nil))
		} else {
			in, err = s.EnsureSQLInstance(ctx, spec, state.guarded(s, job.Binding == nil))
		}
		if err != nil {
			return nil, err
		}
		if saveErr := state.advance("database"); saveErr != nil {
			return nil, saveErr
		}
	} else {
		in, err = s.GetSQLInstance(ctx, instance)
		if err != nil {
			return nil, err
		}
	}
	if inspectErr := state.inspect(in, job.Binding == nil); inspectErr != nil {
		return nil, inspectErr
	}
	if in.DatabaseVersion != version || in.State != "RUNNABLE" {
		return nil, errors.New("cloud sql instance no longer matches the saved deployment")
	}
	if provisionErr := state.ensurePSC(ctx, s, cfg); provisionErr != nil {
		return nil, provisionErr
	}
	var endpoint *gcp.SQLSourceEndpoint
	if psc {
		endpoint, err = s.ReadSQLPITRSourceEndpoint(ctx, spec, state.ResourceID, cfg.Managed.CloudSQLPSC, state.guarded(s, job.Binding == nil).Check)
		if err != nil {
			return nil, err
		}
	}
	db := databaseName(job.Action.Name)
	if state.Stage == "database" {
		if err = s.EnsureDatabase(ctx, instance, db, state.guarded(s, job.Binding == nil)); err != nil {
			return nil, fmt.Errorf("create the database: %w", err)
		}
		if saveErr := state.advance("user"); saveErr != nil {
			return nil, saveErr
		}
	}
	if password == "" && state.Stage == "user" && state.Operation == "" {
		password, err = keptPassword(ctx, job, rand.Text()+rand.Text())
	} else if password == "" {
		password, err = originalSQLPassword(ctx, job)
	}
	if err != nil {
		return nil, err
	}
	if state.Stage == "user" {
		if err = s.SetUser(ctx, instance, sqlUser, password, state.guarded(s, job.Binding == nil)); err != nil {
			if psc {
				return nil, cloudSQLPSCCredentialError{err}
			}
			return nil, fmt.Errorf("set the database user: %w", err)
		}
		if saveErr := state.advance("ready"); saveErr != nil {
			return nil, saveErr
		}
	}
	if psc {
		if err := c.verifyPSCSource(ctx, s, job, state, spec, endpoint, db, password); err != nil {
			return nil, err
		}
	}
	if err := job.Keep(ctx, ResourceURL, []byte(databaseURL(cfg.Kind, password, db, in.ConnectionName))); err != nil {
		return nil, err
	}

	owned, ownershipErr := state.ownership(job.Binding)
	if ownershipErr != nil {
		return nil, ownershipErr
	}
	return &Outcome{ExternalID: in.ConnectionName, PhysicalID: state.ResourceID, Revision: in.DatabaseVersion, OwnedResources: owned}, nil
}

func originalSQLPassword(ctx context.Context, job *Job) (string, error) {
	kept, err := job.Recall(ctx, passwordField)
	if errors.Is(err, ErrNoCredential) || (err == nil && len(kept) == 0) {
		return "", errors.New("the original cloud sql instance has no kept password")
	}
	return string(kept), err
}

// keptPassword is a database user's password: the one kept for this
// database, or fresh, kept before it's used so a retry sets the same one.
func keptPassword(ctx context.Context, job *Job, fresh string) (password string, err error) {
	kept, err := job.Recall(ctx, passwordField)
	if err == nil && len(kept) > 0 {
		return string(kept), nil
	}
	if err != nil && !errors.Is(err, ErrNoCredential) {
		return "", err
	}
	if err := job.Keep(ctx, passwordField, []byte(fresh)); err != nil {
		return "", err
	}
	return fresh, nil
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

// Remove implements Executor: it requests a final backup before instance
// removal and forgets credentials only after the operation succeeds.
func (c *CloudSQL) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	cfg, err := managedDatabaseBindingReview(b, job.Connection, "cloud-sql")
	if err != nil {
		return err
	}
	key, err := gcp.ParseKey(job.Credential)
	if err != nil {
		return err
	}
	if cfg.Managed != nil && key.ProjectID != job.Connection.Details["project_id"] {
		return fmt.Errorf("managed database review changed: the credential belongs to a different project")
	}
	instance, region, err := boundSQLInstance(b.ExternalID, key.ProjectID, b.Region)
	if err != nil {
		return err
	}
	removing := *job
	removing.Binding = b
	state, err := readCloudSQLState(&removing, cfg, key.ProjectID, region, instance, "remove")
	if err != nil {
		return err
	}
	if b.PhysicalID != "" {
		if state.ResourceID != "" && state.ResourceID != b.PhysicalID {
			return errors.New("cloud sql checkpoint incarnation changed")
		}
		state.ResourceID = b.PhysicalID
	}
	if state.Stage != "removed" {
		s, sessionErr := c.Google.Session(ctx, key)
		if sessionErr != nil {
			return sessionErr
		}
		ctx = progress.With(ctx, func(m string) { job.Progress(StepDeploying, m) })
		if err := s.DeleteSQLInstance(ctx, instance, state.guarded(s, false)); err != nil {
			return err
		}
		if err := state.advance("removed"); err != nil {
			return err
		}
	}

	if state.PSC != nil {
		client, sessionErr := c.Google.Session(ctx, key)
		if sessionErr != nil {
			return sessionErr
		}
		cleanup := gcp.SQLPSCCleanupState{}
		if state.PSCCleanup != nil {
			cleanup = *state.PSCCleanup
		}
		if err := client.DeleteSQLPSCSourceEndpoint(ctx, cloudSQLProvisionSpec(cfg, instance, state.ResourceID), state.PSC.Provision, state.PSC.Endpoint, &cleanup, func(next gcp.SQLPSCCleanupState) error { state.PSCCleanup = &next; return state.save() }, state.pscCheck); err != nil {
			return err
		}
	}
	return job.Forget(ctx)
}

// Observe implements Executor.
func (c *CloudSQL) Observe(ctx context.Context, conn *connections.Connection, credential []byte, b *planning.Binding) (*Observation, error) {
	cfg, err := managedDatabaseBindingReview(b, conn, "cloud-sql")
	if err != nil {
		return nil, err
	}
	key, err := gcp.ParseKey(credential)
	if err != nil {
		return nil, err
	}
	if cfg.Managed != nil && key.ProjectID != conn.Details["project_id"] {
		return nil, fmt.Errorf("managed database review changed: the credential belongs to a different project")
	}
	owned, err := bindingCloudSQLPSC(cfg, b, key.ProjectID)
	if err != nil {
		return nil, err
	}
	s, err := c.Google.Session(ctx, key)
	if err != nil {
		return nil, err
	}
	instance, region, err := boundSQLInstance(b.ExternalID, key.ProjectID, b.Region)
	if err != nil {
		return nil, err
	}
	in, err := s.GetSQLInstance(ctx, instance)
	if gcp.IsNotFound(err) {
		return &Observation{}, nil
	}
	if err != nil {
		return nil, err
	}
	pin, err := in.Incarnation(key.ProjectID, region, instance)
	if err != nil {
		return nil, err
	}
	if b.PhysicalID != "" && pin != b.PhysicalID {
		return nil, errors.New("cloud sql incarnation changed")
	}
	if err := owned.verify(ctx, s, cfg, instance, b.PhysicalID, func(ctx context.Context) error { return ctx.Err() }); err != nil {
		return nil, err
	}
	if cfg.Managed != nil && cfg.Managed.CloudSQLPSC != (manageddb.CloudSQLSourceEndpoint{}) {
		spec := cloudSQLPITRSourceSpec(cfg, instance)
		if _, err := s.ReadSQLPITRSourceEndpoint(ctx, spec, b.PhysicalID, cfg.Managed.CloudSQLPSC, func(ctx context.Context) error { return ctx.Err() }); err != nil {
			return nil, err
		}
	}
	return &Observation{Exists: true, Revision: in.DatabaseVersion}, nil
}

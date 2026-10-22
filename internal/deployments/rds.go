package deployments

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/progress"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

// rdsEngines maps a resource kind to its RDS engine and port.
var rdsEngines = map[string]struct {
	engine string
	port   int32
}{
	"postgres": {"postgres", 5432},
	"mysql":    {"mysql", 3306},
}

// RDS provisions Postgres and MySQL on Amazon RDS in the account's
// default VPC: an instance, not publicly reachable, whose security group
// lets in the environment's ECS tasks and nothing else, and an app user
// whose password Loomplane generates and keeps in Vault.
type RDS struct {
	AWS *aws.Client
}

var _ Executor = (*RDS)(nil)

// RetainCheckpointOnRetry preserves physical identity and removal proof.
func (*RDS) RetainCheckpointOnRetry() bool { return true }

func (r *RDS) session(conn *connections.Connection, cred []byte) (*aws.Session, error) {
	key, err := aws.ParseKey(cred)
	if err != nil {
		return nil, err
	}
	return r.AWS.Session(key, conn.Details["region"], conn.Account), nil
}

// rdsRevision is what a binding records of a database: its engine and
// major version, so RDS's own minor upgrades aren't drift.
func rdsRevision(engine, version string) string {
	parts := strings.Split(version, ".")
	major := parts[0]
	if engine == "mysql" && len(parts) > 1 {
		major += "." + parts[1]
	}
	return engine + "/" + major
}

// Apply implements Executor.
func (r *RDS) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	cfg := job.Action.Resource
	if cfg == nil {
		return nil, fmt.Errorf("%w: rds provisions databases, not %s", ErrNoExecutor, job.Action.Subject)
	}
	request, err := managedDatabaseRequest(job, "rds")
	if err != nil {
		return nil, err
	}
	eng, ok := rdsEngines[cfg.Kind]
	if !ok || cfg.Version == "" {
		return nil, fmt.Errorf("rds doesn't offer %s %s", cfg.Kind, cfg.Version)
	}
	instance := SQLInstanceName(job)
	physical := ""
	existing := job.Binding != nil
	if existing {
		instance = job.Binding.ExternalID
		physical = job.Binding.PhysicalID
	}
	state, err := readRDSState(job, cfg, instance, cfg.Region, physical, "apply")
	if err != nil {
		return nil, err
	}
	var password string
	if state.Attempted || state.ResourceID != "" || existing {
		kept, recallErr := job.Recall(ctx, passwordField)
		if recallErr != nil && !errors.Is(recallErr, ErrNoCredential) {
			return nil, recallErr
		}
		if len(kept) == 0 {
			return nil, errors.New("the original RDS database has no kept password; use an explicit credential recovery operation")
		}
		password = string(kept)
	} else {
		var passwordErr error
		password, passwordErr = keptPassword(ctx, job, rand.Text())
		if passwordErr != nil {
			return nil, passwordErr
		}
	}
	s, err := r.session(job.Connection, job.Credential)
	if err != nil {
		return nil, err
	}
	ctx = progress.With(ctx, func(m string) { job.Progress(StepDeploying, m) })

	net, err := s.DefaultNetwork(ctx)
	if err != nil {
		return nil, err
	}
	env := awsEnvironment(job)
	tasks, err := tasksGroup(ctx, s, net.VPC, env)
	if err != nil {
		return nil, err
	}
	group, err := s.EnsureSecurityGroup(ctx, net.VPC, "loomplane-"+env+"-db", "Loomplane databases for "+env)
	if err != nil {
		return nil, err
	}
	if aerr := s.AllowPortFromGroup(ctx, group, tasks, eng.port); aerr != nil {
		return nil, aerr
	}
	subnets := "loomplane-" + net.VPC
	if serr := s.EnsureDBSubnetGroup(ctx, subnets, net.Subnets); serr != nil {
		return nil, serr
	}
	job.Progress(StepDeploying, "Creating the database instance; this takes several minutes")
	db := databaseName(job.Action.Name)
	in, err := s.EnsureDBInstance(ctx, aws.DBInstanceSpec{
		ResourceID: state.ResourceID, Intent: state.Intent, Existing: existing,
		ID: instance, Engine: eng.engine, EngineVersion: cfg.Version, Class: request.ComputeClass,
		StorageGB: request.StorageGB, Database: db, User: sqlUser, Password: password,
		SubnetGroup: subnets, Group: group,
	}, state.createTrack())
	if err != nil {
		return nil, err
	}
	state.Stage = "ready"
	if saveErr := state.save(); saveErr != nil {
		return nil, saveErr
	}
	addr := rdsURL(cfg.Kind, password, in.Address, in.Port, db)
	if err := job.Keep(ctx, ResourceURL, []byte(addr)); err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: in.ID, PhysicalID: in.ResourceID, Revision: rdsRevision(in.Engine, in.EngineVersion)}, nil
}

// rdsURL is how a task reaches the database, over TLS, which RDS for
// Postgres requires.
func rdsURL(kind, password, host string, port int32, db string) string {
	creds := url.UserPassword(sqlUser, password).String()
	hostPort := host + ":" + strconv.Itoa(int(port))
	if kind == "mysql" {
		return "mysql://" + creds + "@" + hostPort + "/" + db + "?tls=true"
	}
	return "postgresql://" + creds + "@" + hostPort + "/" + db + "?sslmode=require"
}

// Remove retains a final snapshot and automated backups before forgetting
// the instance credentials. Snapshot failure keeps the binding retryable.
func (r *RDS) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	cfg, err := managedDatabaseBindingReview(b, job.Connection, "rds")
	if err != nil {
		return err
	}
	region := b.Region
	if region == "" {
		region = job.Connection.Details["region"]
	}
	state, err := readRDSState(job, cfg, b.ExternalID, region, b.PhysicalID, "remove")
	if err != nil {
		return err
	}
	if state.Stage != "removed" {
		s, sessionErr := r.session(job.Connection, job.Credential)
		if sessionErr != nil {
			return sessionErr
		}
		ctx = progress.With(ctx, func(m string) { job.Progress(StepDeploying, m) })
		if state.ResourceID == "" {
			db, readErr := s.GetDBInstance(ctx, b.ExternalID)
			if readErr != nil {
				return readErr
			}
			if captureErr := state.capture(db.ResourceID); captureErr != nil {
				return captureErr
			}
		}
		point, removeErr := s.DeleteDBInstance(ctx, aws.DBRemovalSpec{ID: b.ExternalID, ResourceID: state.ResourceID, SnapshotID: aws.FinalDBSnapshotID(state.ResourceID)}, state.removeTrack())
		if removeErr != nil {
			return removeErr
		}
		state.Snapshot = point
		state.Stage = "removed"
		if saveErr := state.save(); saveErr != nil {
			return saveErr
		}
	}
	return job.Forget(ctx)
}

// Observe implements Executor.
func (r *RDS) Observe(ctx context.Context, conn *connections.Connection, credential []byte, b *planning.Binding) (*Observation, error) {
	if _, err := managedDatabaseBindingReview(b, conn, "rds"); err != nil {
		return nil, err
	}
	s, err := r.session(conn, credential)
	if err != nil {
		return nil, err
	}
	in, err := s.GetDBInstance(ctx, b.ExternalID)
	if aws.IsNotFound(err) {
		return &Observation{}, nil
	}
	if err != nil {
		return nil, err
	}
	if b.PhysicalID != "" && in.ResourceID != b.PhysicalID {
		return nil, errors.New("RDS database physical identity changed; review its ownership")
	}
	if in.Status == "deleting" {
		return nil, errors.New("RDS database removal is still in progress")
	}
	return &Observation{Exists: true, Revision: rdsRevision(in.Engine, in.EngineVersion)}, nil
}

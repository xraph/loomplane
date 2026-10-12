package deployments

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/progress"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

const (
	// rdsClass is the instance an RDS database runs on: two burstable
	// vCPUs and 1 GB on Graviton, the smallest class.
	rdsClass = "db.t4g.micro"
	// rdsStorageGB is the instance's gp3 disk, RDS's smallest.
	rdsStorageGB = 20
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
	eng, ok := rdsEngines[cfg.Kind]
	if !ok || cfg.Version == "" {
		return nil, fmt.Errorf("rds doesn't offer %s %s", cfg.Kind, cfg.Version)
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
	// RDS takes MySQL passwords of up to 41 characters, so one rand.Text
	// (26, 130 bits) and not Cloud SQL's two.
	password, isNew, err := keptPassword(ctx, job, rand.Text())
	if err != nil {
		return nil, err
	}

	job.Progress(StepDeploying, "Creating the database instance; this takes several minutes")
	db := databaseName(job.Action.Name)
	in, created, err := s.EnsureDBInstance(ctx, aws.DBInstanceSpec{
		ID: SQLInstanceName(job), Engine: eng.engine, EngineVersion: cfg.Version, Class: rdsClass,
		StorageGB: rdsStorageGB, Database: db, User: sqlUser, Password: password,
		SubnetGroup: subnets, Group: group,
	})
	if err != nil {
		return nil, err
	}
	// An instance that outlived its kept password gets the new one.
	if isNew && !created {
		if perr := s.SetDBPassword(ctx, in.ID, password); perr != nil {
			return nil, perr
		}
	}
	addr := rdsURL(cfg.Kind, password, in.Address, in.Port, db)
	if err := job.Keep(ctx, ResourceURL, []byte(addr)); err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: in.ID, Revision: rdsRevision(in.Engine, in.EngineVersion)}, nil
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

// Remove implements Executor: it deletes the instance, with its data and
// backups, and the credentials Loomplane kept for it.
func (r *RDS) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	s, err := r.session(job.Connection, job.Credential)
	if err != nil {
		return err
	}
	if err := s.DeleteDBInstance(ctx, b.ExternalID); err != nil {
		return err
	}
	return job.Forget(ctx)
}

// Observe implements Executor.
func (r *RDS) Observe(ctx context.Context, conn *connections.Connection, credential []byte, b *planning.Binding) (*Observation, error) {
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
	if in.Status == "deleting" {
		return &Observation{}, nil
	}
	return &Observation{Exists: true, Revision: rdsRevision(in.Engine, in.EngineVersion)}, nil
}

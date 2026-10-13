package deployments

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/url"
	"strconv"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

// cacheNode is the node an ElastiCache Redis runs on: two burstable vCPUs
// and half a gigabyte on Graviton, the smallest.
const cacheNode = "cache.t4g.micro"

// ElastiCache provisions Redis on Amazon ElastiCache in the account's
// default VPC: one node, encrypted in transit and at rest, with an auth
// token Loomplane generates and keeps in Vault, reachable from the
// environment's ECS tasks only.
type ElastiCache struct {
	AWS *aws.Client
}

var _ Executor = (*ElastiCache)(nil)

func (e *ElastiCache) session(conn *connections.Connection, cred []byte) (*aws.Session, error) {
	key, err := aws.ParseKey(cred)
	if err != nil {
		return nil, err
	}
	return e.AWS.Session(key, conn.Details["region"], conn.Account), nil
}

// cacheID is a replication group's ID: at most 40 characters, starting
// with a letter.
func cacheID(job *Job) string { return shortName(job.Action.Name + "-" + awsEnvironment(job)) }

const cacheRevision = "redis"

// Apply implements Executor.
func (e *ElastiCache) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	if job.Action.Resource == nil || job.Action.Resource.Kind != "redis" {
		return nil, fmt.Errorf("%w: elasticache provisions redis, not %s", ErrNoExecutor, job.Action.Subject)
	}
	s, err := e.session(job.Connection, job.Credential)
	if err != nil {
		return nil, err
	}
	net, err := s.DefaultNetwork(ctx)
	if err != nil {
		return nil, err
	}
	env := awsEnvironment(job)
	tasks, err := tasksGroup(ctx, s, net.VPC, env)
	if err != nil {
		return nil, err
	}
	group, err := s.EnsureSecurityGroup(ctx, net.VPC, "loomplane-"+env+"-cache", "Loomplane caches for "+env)
	if err != nil {
		return nil, err
	}
	if aerr := s.AllowPortFromGroup(ctx, group, tasks, 6379); aerr != nil {
		return nil, aerr
	}
	subnets := "loomplane-" + net.VPC
	if serr := s.EnsureCacheSubnetGroup(ctx, subnets, net.Subnets); serr != nil {
		return nil, serr
	}
	// The token is set when the cache is made, so it's kept first and a
	// retry reads the same one back.
	token, _, err := keptPassword(ctx, job, rand.Text())
	if err != nil {
		return nil, err
	}
	job.Progress(StepDeploying, "Creating the cache; this takes several minutes")
	c, err := s.EnsureCache(ctx, aws.CacheSpec{ID: cacheID(job), NodeType: cacheNode, AuthToken: token, SubnetGroup: subnets, Group: group})
	if err != nil {
		return nil, err
	}
	addr := "rediss://" + url.UserPassword("", token).String() + "@" + c.Address + ":" + strconv.Itoa(int(c.Port))
	if err := job.Keep(ctx, ResourceURL, []byte(addr)); err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: c.ID, Revision: cacheRevision}, nil
}

// Remove implements Executor: it deletes the cache and the token kept for
// it.
func (e *ElastiCache) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	s, err := e.session(job.Connection, job.Credential)
	if err != nil {
		return err
	}
	if err := s.DeleteCache(ctx, b.ExternalID); err != nil {
		return err
	}
	return job.Forget(ctx)
}

// Observe implements Executor.
func (e *ElastiCache) Observe(ctx context.Context, conn *connections.Connection, cred []byte, b *planning.Binding) (*Observation, error) {
	s, err := e.session(conn, cred)
	if err != nil {
		return nil, err
	}
	c, err := s.GetCache(ctx, b.ExternalID)
	if aws.IsNotFound(err) {
		return &Observation{}, nil
	}
	if err != nil {
		return nil, err
	}
	if c.Status == "deleting" {
		return &Observation{}, nil
	}
	return &Observation{Exists: true, Revision: cacheRevision}, nil
}

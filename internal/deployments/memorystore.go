package deployments

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

// Memorystore provisions Redis on Google Memorystore: a Basic tier
// instance on the project's default network, with AUTH on. It has a
// private address only, so Cloud Run services that need it deploy with
// Direct VPC egress to that network.
type Memorystore struct {
	Google *gcp.Client
}

var _ Executor = (*Memorystore)(nil)

const memorystoreRevision = "redis"

func (m *Memorystore) session(ctx context.Context, cred []byte) (*gcp.Session, error) {
	key, err := gcp.ParseKey(cred)
	if err != nil {
		return nil, err
	}
	return m.Google.Session(ctx, key)
}

// redisRef is how a binding names an instance: its region and ID, both of
// which deleting and reading it need.
func redisRef(b *planning.Binding) (region, id string, err error) {
	region, id, ok := strings.Cut(b.ExternalID, "/")
	if !ok || region == "" || id == "" {
		return "", "", fmt.Errorf("the binding names no instance: %q", b.ExternalID)
	}
	return region, id, nil
}

// Apply implements Executor.
func (m *Memorystore) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	cfg := job.Action.Resource
	if cfg == nil || cfg.Kind != "redis" {
		return nil, fmt.Errorf("%w: memorystore provisions redis, not %s", ErrNoExecutor, job.Action.Subject)
	}
	s, err := m.session(ctx, job.Credential)
	if err != nil {
		return nil, err
	}
	// An instance ID is lower case letters, digits and hyphens, starting
	// with a letter, at most 40 long.
	id := shortName(job.Action.Name + "-" + job.Deployment.Environment + "-" + idSuffix(job.Deployment.ProjectID))
	job.Progress(StepDeploying, "Creating the cache; this takes several minutes")
	in, err := s.EnsureRedis(ctx, cfg.Region, id)
	if err != nil {
		return nil, err
	}
	auth, err := s.RedisAuth(ctx, cfg.Region, id)
	if err != nil {
		return nil, err
	}
	addr := "redis://" + url.UserPassword("", auth).String() + "@" + in.Host + ":" + strconv.Itoa(in.Port)
	if err := job.Keep(ctx, ResourceURL, []byte(addr)); err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: cfg.Region + "/" + id, Revision: memorystoreRevision}, nil
}

// Remove implements Executor: it deletes the instance and its data.
func (m *Memorystore) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	region, id, err := redisRef(b)
	if err != nil {
		return err
	}
	s, err := m.session(ctx, job.Credential)
	if err != nil {
		return err
	}
	if err := s.DeleteRedis(ctx, region, id); err != nil {
		return err
	}
	return job.Forget(ctx)
}

// Observe implements Executor.
func (m *Memorystore) Observe(ctx context.Context, _ *connections.Connection, cred []byte, b *planning.Binding) (*Observation, error) {
	region, id, err := redisRef(b)
	if err != nil {
		return nil, err
	}
	s, err := m.session(ctx, cred)
	if err != nil {
		return nil, err
	}
	in, err := s.GetRedis(ctx, region, id)
	if gcp.IsNotFound(err) {
		return &Observation{}, nil
	}
	if err != nil {
		return nil, err
	}
	if in.State == "DELETING" {
		return &Observation{}, nil
	}
	return &Observation{Exists: true, Revision: memorystoreRevision}, nil
}

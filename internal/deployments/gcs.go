package deployments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

// CloudStorage provisions object storage on Google Cloud Storage: a
// private bucket the project's Cloud Run services reach as their runtime
// account. Services get STORAGE_URL, gs://bucket, and Google's client
// libraries find the account's credentials themselves.
type CloudStorage struct {
	Google *gcp.Client
}

var _ Executor = (*CloudStorage)(nil)

const gcsRevision = "gcs"

// gcsBucket is a resource's bucket: the one its binding names, or a new
// one's name, lower case, at most 63 long, unique across Google Cloud by
// a hash of the project and environment. A new one's name has its region
// in it. A bucket's location can't change and its name is global, so a
// move creates another bucket, and it needs a name the old one doesn't
// hold.
func gcsBucket(project string, job *Job) string {
	if b := job.Binding; b != nil && b.ExternalID != "" {
		return b.ExternalID
	}
	sum := sha256.Sum256([]byte(project + "\x00" + job.Deployment.ProjectID + "\x00" + job.Deployment.Environment))
	suffix := "-" + hex.EncodeToString(sum[:])[:10]
	if region := strings.ToLower(job.Action.Resource.Region); region != "" {
		suffix = "-" + region + suffix
	}
	room := 63 - len("lp-") - len(suffix)
	name := job.Action.Name
	if len(name) > room {
		name = name[:room]
	}
	return "lp-" + name + suffix
}

func (c *CloudStorage) session(ctx context.Context, cred []byte) (*gcp.Session, error) {
	key, err := gcp.ParseKey(cred)
	if err != nil {
		return nil, err
	}
	return c.Google.Session(ctx, key)
}

// Apply implements Executor.
func (c *CloudStorage) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	cfg := job.Action.Resource
	if cfg == nil || cfg.Kind != "object-storage" {
		return nil, fmt.Errorf("%w: cloud storage provisions object storage, not %s", ErrNoExecutor, job.Action.Subject)
	}
	s, err := c.session(ctx, job.Credential)
	if err != nil {
		return nil, err
	}
	name := gcsBucket(s.Key().ProjectID, job)
	job.Progress(StepDeploying, "Creating bucket "+name)
	if berr := s.EnsurePrivateBucket(ctx, name, strings.ToUpper(cfg.Region)); berr != nil {
		return nil, berr
	}
	account, err := s.RuntimeAccount(ctx)
	if err != nil {
		return nil, fmt.Errorf("find the runtime service account: %w", err)
	}
	if gerr := s.GrantBucket(ctx, name, account, "roles/storage.objectAdmin"); gerr != nil {
		return nil, gerr
	}
	if err := job.Keep(ctx, ResourceURL, []byte("gs://"+name)); err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: name, Revision: gcsRevision}, nil
}

// Remove implements Executor: it deletes the bucket and everything in it.
func (c *CloudStorage) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	s, err := c.session(ctx, job.Credential)
	if err != nil {
		return err
	}
	if err := s.DeleteBucket(ctx, b.ExternalID); err != nil {
		return err
	}
	return job.Forget(ctx)
}

// Observe implements Executor.
func (c *CloudStorage) Observe(ctx context.Context, _ *connections.Connection, cred []byte, b *planning.Binding) (*Observation, error) {
	s, err := c.session(ctx, cred)
	if err != nil {
		return nil, err
	}
	ok, err := s.BucketExists(ctx, b.ExternalID)
	if err != nil || !ok {
		return &Observation{}, err
	}
	return &Observation{Exists: true, Revision: gcsRevision}, nil
}

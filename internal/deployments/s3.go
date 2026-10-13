package deployments

import (
	"context"
	"fmt"
	"net/url"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

// S3 provisions object storage on Amazon S3: a private bucket, which the
// environment's ECS tasks reach through their role. There's no password:
// services get STORAGE_URL, s3://bucket?region=..., and the AWS SDK finds
// the role's credentials itself.
type S3 struct {
	AWS *aws.Client
}

var _ Executor = (*S3)(nil)

func (b *S3) session(conn *connections.Connection, cred []byte) (*aws.Session, error) {
	key, err := aws.ParseKey(cred)
	if err != nil {
		return nil, err
	}
	return b.AWS.Session(key, conn.Details["region"], conn.Account), nil
}

// s3Revision is what a binding records of a bucket. Nothing about a
// bucket changes that a plan sets.
const s3Revision = "s3"

// Apply implements Executor.
func (b *S3) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	if job.Action.Resource == nil || job.Action.Resource.Kind != "object-storage" {
		return nil, fmt.Errorf("%w: s3 provisions object storage, not %s", ErrNoExecutor, job.Action.Subject)
	}
	s, err := b.session(job.Connection, job.Credential)
	if err != nil {
		return nil, err
	}
	name := bucketName(s, job)
	job.Progress(StepDeploying, "Creating bucket "+name)
	if err := s.EnsurePrivateBucket(ctx, name); err != nil {
		return nil, err
	}
	// The role exists before any service needs it, so the bucket is
	// reachable as soon as one deploys.
	roleName, rolePolicy := tasksRole(s, job)
	if _, err := s.EnsureRole(ctx, roleName, "ecs-tasks.amazonaws.com", nil, rolePolicy); err != nil {
		return nil, err
	}
	addr := "s3://" + name + "?region=" + url.QueryEscape(s.Region())
	if err := job.Keep(ctx, ResourceURL, []byte(addr)); err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: name, Revision: s3Revision}, nil
}

// Remove implements Executor: it deletes the bucket and everything in it.
func (b *S3) Remove(ctx context.Context, job *Job, binding *planning.Binding) error {
	s, err := b.session(job.Connection, job.Credential)
	if err != nil {
		return err
	}
	if err := s.DeleteBucket(ctx, binding.ExternalID); err != nil {
		return err
	}
	return job.Forget(ctx)
}

// Observe implements Executor.
func (b *S3) Observe(ctx context.Context, conn *connections.Connection, cred []byte, binding *planning.Binding) (*Observation, error) {
	s, err := b.session(conn, cred)
	if err != nil {
		return nil, err
	}
	ok, err := s.BucketExists(ctx, binding.ExternalID)
	if err != nil || !ok {
		return &Observation{}, err
	}
	return &Observation{Exists: true, Revision: s3Revision}, nil
}

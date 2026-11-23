package deployments

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/registry"
	"github.com/xraph/loomplane/internal/connections"
	fc "github.com/xraph/loomplane/internal/firecracker"
)

// codeBuildPrefix is the ECR repository prefix CodeBuild pushes to, as it
// does for ECS. A server that pulls a CodeBuild image does so through an
// ECR registry connection for that prefix.
const codeBuildPrefix = "loomplane"

// ecrHost reads an ECR registry endpoint's account and region.
func ecrHost(endpoint string) (account, region string, ok bool) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", "", false
	}
	parts := strings.Split(u.Host, ".")
	// <account>.dkr.ecr.<region>.amazonaws.com
	if len(parts) != 6 || parts[1] != "dkr" || parts[2] != "ecr" || parts[4] != "amazonaws" || parts[5] != "com" {
		return "", "", false
	}
	return parts[0], parts[3], true
}

// buildOnCodeBuild builds a service a server, node or Firecracker host runs
// on CodeBuild in the reviewed AWS account, and pushes it to that account's
// ECR, where the target pulls it through the reviewed ECR registry. It isn't
// a Loomplane runner: CodeBuild isolates builds itself, in the account.
func (b *ImageBuilder) buildOnCodeBuild(ctx context.Context, job *Job, state *ImageArtifact, save func() error, reg *registry.Config) (string, error) {
	if b.AWS == nil {
		return "", errors.New("CodeBuild builds aren't available here")
	}
	if reg == nil || job.Source == nil || job.SourceID == "" {
		return "", errors.New("a CodeBuild build needs approved source and an ECR registry")
	}
	target := job.Action.Service.BuildTarget
	conn, raw, err := b.connection(ctx, job, target.Builder, connections.AWS, target.BuilderSettings, false)
	if err != nil {
		return "", err
	}
	account, region, ok := ecrHost(reg.Endpoint)
	if !ok || account != conn.Details["account_id"] || reg.Repository != codeBuildPrefix {
		return "", fmt.Errorf("a CodeBuild build pushes to the %s ECR repositories of its own account: choose an ECR registry connection for %s with the prefix %s", codeBuildPrefix, conn.Details["account_id"], codeBuildPrefix)
	}
	if state.Reference != "" {
		if state.SourceID != job.SourceID || state.Builder != target.Builder || state.Registry != target.Registry {
			return "", fc.ErrConflict
		}
		return state.Reference, nil
	}
	key, err := aws.ParseKey(raw)
	if err != nil {
		return "", err
	}
	s := b.AWS.Session(key, region, account)
	state.SourceID, state.Builder, state.Registry, state.Architecture = job.SourceID, target.Builder, target.Registry, "amd64"
	es := ecsState{BuildID: state.Operation, LogAttempt: state.Attempt}
	image, err := (&ECS{}).build(ctx, s, job, &es, func() {
		state.Operation, state.Attempt = es.BuildID, es.LogAttempt
		_ = save() //nolint:errcheck // the next checkpoint retries it
	})
	state.Operation, state.Attempt = es.BuildID, es.LogAttempt
	if err != nil {
		return "", err
	}
	if es.Unpinned != "" {
		job.Warn(unpinnedWarning(job.Action.Name, es.Unpinned))
	}
	state.Reference = image
	_, state.Digest, _ = strings.Cut(image, "@")
	if saveErr := save(); saveErr != nil {
		return "", saveErr
	}
	return image, nil
}

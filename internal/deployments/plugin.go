package deployments

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/plugins"
	"github.com/xraph/loomplane/pkg/provider/sdk"
)

// Plugin carries out a provider plugin's actions over its protocol: it
// uploads the source, starts an operation, and reads it until it's done,
// passing its progress and checkpoints on. The plugin does the work, on
// its own infrastructure.
type Plugin struct {
	Client *plugins.Client
	// Every is how often an operation is read. Zero means a second.
	Every time.Duration
}

var _ Executor = (*Plugin)(nil)

func (p *Plugin) every() time.Duration {
	if p.Every > 0 {
		return p.Every
	}
	return time.Second
}

// credential is what the plugin gets of a connection: the secret, and the
// settings and details it returned when it was checked.
func credential(conn *connections.Connection, secret []byte) (sdk.Credential, sdk.Account) {
	return sdk.Credential{Secret: secret, Settings: maps.Clone(conn.Details)},
		sdk.Account{Account: conn.Account, Details: maps.Clone(conn.Details), Missing: conn.Missing}
}

func target(b *planning.Binding, cred sdk.Credential, acct sdk.Account) *sdk.Target {
	if b == nil {
		return nil
	}
	return &sdk.Target{ExternalID: b.ExternalID, Revision: b.Revision, URL: b.URL, Credential: cred, Account: acct}
}

// sdkJob is the job as the plugin sees it.
func sdkJob(job *Job) *sdk.Job {
	cred, acct := credential(job.Connection, job.Credential)
	j := &sdk.Job{
		Key:     job.Deployment.ID + ":" + job.Action.ID,
		Project: job.Deployment.ProjectID, Environment: job.Deployment.Environment,
		Name: job.Action.Name, Region: job.Action.Region, Commit: job.CommitSHA,
		Credential: cred, Account: acct, Current: target(job.Binding, cred, acct), Resume: job.Resume,
	}
	if cfg := job.Action.Service; cfg != nil {
		s := &sdk.Service{
			Source: cfg.Source, Ecosystem: string(cfg.Ecosystem), Framework: cfg.Framework, Dockerfile: cfg.Image,
			Build: cfg.Build, Start: cfg.Start, Workload: string(cfg.Workload), Port: cfg.Port, Public: cfg.Public,
			Env: job.Env, Secrets: slices.Sorted(maps.Keys(job.Secrets)),
		}
		if cfg.Image != "" {
			s.Context = cfg.ContextDir()
		}
		if cfg.Scale != nil {
			s.MinScale, s.MaxScale = cfg.Scale.Min, cfg.Scale.Max
		}
		if cfg.Size != nil {
			s.CPU, s.MemoryMB = cfg.Size.CPU, cfg.Size.Memory
		}
		j.Service = s
	}
	if cfg := job.Action.Resource; cfg != nil {
		j.Resource = &sdk.Resource{Kind: cfg.Kind, Version: cfg.Version}
	}
	return j
}

// Apply implements Executor.
func (p *Plugin) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	req := sdk.OperationRequest{Kind: sdk.OpApply, Job: *sdkJob(job)}
	if job.Action.Service != nil {
		if err := p.upload(ctx, job); err != nil {
			return nil, err
		}
		req.Source = job.SourceID
	}
	op, err := p.run(ctx, job, req)
	if err != nil {
		return nil, err
	}
	out := op.Outcome
	if out == nil {
		return nil, errors.New("the plugin finished without saying what it made")
	}
	if out.Address != "" {
		if err := job.Keep(ctx, ResourceURL, []byte(out.Address)); err != nil {
			return nil, err
		}
	}
	return &Outcome{ExternalID: out.ExternalID, URL: out.URL, Image: out.Image, Revision: out.Revision}, nil
}

// upload sends the source, unless the plugin has it from an earlier
// attempt or another service.
func (p *Plugin) upload(ctx context.Context, job *Job) error {
	has, err := p.Client.HasSource(ctx, job.SourceID)
	if err != nil || has {
		return err
	}
	job.Progress(StepBuilding, "Uploading the source to the provider")
	src, _, err := job.Source(ctx)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := p.Client.PutSource(ctx, job.SourceID, src); err != nil {
		return fmt.Errorf("upload the source: %w", err)
	}
	return nil
}

// Remove implements Executor.
func (p *Plugin) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	j := sdkJob(job)
	j.Current = target(b, j.Credential, j.Account)
	if _, err := p.run(ctx, job, sdk.OperationRequest{Kind: sdk.OpRemove, Job: *j}); err != nil {
		return err
	}
	if job.Action.Subject == planning.ResourceSubject {
		return job.Forget(ctx)
	}
	return nil
}

// run starts an operation and reads it until it's done. Each reading
// passes the plugin's checkpoint on, which is also the step's heartbeat.
func (p *Plugin) run(ctx context.Context, job *Job, req sdk.OperationRequest) (*sdk.Operation, error) {
	op, err := p.Client.Start(ctx, req)
	if err != nil {
		return nil, err
	}
	last, checkpoint := "", job.Resume
	for {
		switch op.Status {
		case sdk.StatusSucceeded:
			return op, nil
		case sdk.StatusFailed:
			return nil, fmt.Errorf("the provider failed: %s", op.Error)
		}
		if op.Progress != "" && op.Progress != last {
			last = op.Progress
			job.Progress(StepDeploying, op.Progress)
		}
		if len(op.Checkpoint) > 0 {
			checkpoint = op.Checkpoint
		}
		_ = job.Checkpoint(checkpoint) //nolint:errcheck // the next reading retries it
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(p.every()):
		}
		next, err := p.Client.Operation(ctx, op.ID)
		if errors.Is(err, plugins.ErrNotFound) {
			// The plugin restarted and lost it; start it again, from the
			// last checkpoint.
			req.Job.Resume = checkpoint
			next, err = p.Client.Start(ctx, req)
		}
		if err != nil {
			return nil, err
		}
		op = next
	}
}

// Observe implements Executor.
func (p *Plugin) Observe(ctx context.Context, conn *connections.Connection, secret []byte, b *planning.Binding) (*Observation, error) {
	cred, acct := credential(conn, secret)
	o, err := p.Client.Observe(ctx, *target(b, cred, acct))
	if err != nil {
		return nil, err
	}
	return &Observation{Exists: o.Exists, Revision: o.Revision}, nil
}

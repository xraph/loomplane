package agent

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

var ErrCleanup = errors.New("agent cleanup failed")

type launcherSource interface {
	CredentialSource
	Close() error
}
type launcherTransport interface {
	RuntimeTransport
	Close()
}
type launchDependencies struct {
	openSource func(context.Context, string, string) (launcherSource, error)
	transport  func(string, string, CredentialSource, domain.AgentIdentity) (launcherTransport, error)
	slices     func(string) (runtimecatalog.EndpointSliceClient, func(), error)
}

// RunConfigured joins the observer, signed receiver and authenticated transport.
// It opens no inbound ports and requires a fresh enrollment for every process.
func RunConfigured(ctx context.Context, cfg LaunchConfig) error {
	return launch(ctx, cfg, launchDependencies{
		openSource: func(ctx context.Context, address, uri string) (launcherSource, error) {
			return workloadidentity.OpenSource(ctx, address, uri)
		},
		transport: func(endpoint, control string, source CredentialSource, identity domain.AgentIdentity) (launcherTransport, error) {
			return NewTransport(endpoint, control, source, identity)
		},
		slices: inClusterSlices,
	})
}
func launch(ctx context.Context, cfg LaunchConfig, deps launchDependencies) (err error) {
	if stepErr := cfg.Validate(time.Now()); stepErr != nil {
		return stepErr
	}
	if stepErr := ctx.Err(); stepErr != nil {
		return stepErr
	}
	trust, err := cfg.snapshotTrust(time.Now())
	if err != nil {
		return err
	}
	state, err := OpenStore(cfg.StatePath)
	if err != nil {
		return err
	}
	defer func() { err = cleanupResult(err, state.Close()) }()
	deadline := time.Now().Add(30 * time.Second)
	if cfg.EnrollmentExpiresAt.Before(deadline) {
		deadline = cfg.EnrollmentExpiresAt
	}
	startup, cancel := context.WithDeadline(ctx, deadline)
	source, err := deps.openSource(startup, cfg.WorkloadAPI, cfg.Registration.Agent.URI)
	cancel()
	if err != nil {
		return err
	}
	defer func() { err = cleanupResult(err, source.Close()) }()
	audience, err := workloadidentity.ControlURI(cfg.Registration.Agent)
	if err != nil {
		return err
	}
	transport, err := deps.transport(cfg.ControlEndpoint, audience, source, cfg.Registration.Agent)
	if err != nil {
		return err
	}
	defer transport.Close()
	observer := &runtimecatalog.KubernetesObserver{Namespace: cfg.Namespace, Registration: cfg.Registration, Targets: cfg.Targets}
	if len(cfg.Targets) > 0 {
		client, closeClient, clientErr := deps.slices(cfg.Namespace)
		if clientErr != nil {
			return clientErr
		}
		defer closeClient()
		observer.Client = client
	}
	receiver, err := NewSnapshotReceiver(state, cfg.Registration.Agent, trust)
	if err != nil {
		return err
	}
	if stepErr := cfg.Validate(time.Now()); stepErr != nil {
		return stepErr
	}
	coordinator := &Agent{State: state, Transport: transport, Identity: cfg.Registration.Agent, EnrollmentID: cfg.EnrollmentID, Receive: receiver.Apply, Observe: func(run context.Context, next func(context.Context) (uint64, error), publish func(context.Context, domain.ObservationBatch) error) error {
		observer.NextSequence = next
		observer.Publish = publish
		return observer.Run(run)
	}}
	return coordinator.Run(ctx)
}

func cleanupResult(result, closeErr error) error {
	if closeErr == nil {
		return result
	}
	return errors.Join(result, ErrCleanup, closeErr)
}

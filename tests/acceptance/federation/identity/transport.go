package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	entryv1 "github.com/spiffe/spire-api-sdk/proto/spire/api/server/entry/v1"
	spiretypes "github.com/spiffe/spire-api-sdk/proto/spire/api/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	"github.com/xraph/loomplane/internal/agent"
	"github.com/xraph/loomplane/internal/runtimeapi"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type runtimeAuthority struct {
	identity domain.AgentIdentity
	active   atomic.Bool
	consumed atomic.Bool
}

const fixtureEnrollmentID = "31234567-89ab-cdef-0123-456789abcdef"

func (r *runtimeAuthority) CheckAgent(_ context.Context, a domain.AgentIdentity) error {
	if a != r.identity || !r.active.Load() {
		return workloadidentity.ErrIdentity
	}
	return nil
}
func (r *runtimeAuthority) CheckPending(_ context.Context, id string, a domain.AgentIdentity) error {
	if a != r.identity || id != fixtureEnrollmentID || r.consumed.Load() {
		return workloadidentity.ErrIdentity
	}
	return nil
}
func (r *runtimeAuthority) Consume(ctx context.Context, id string, a domain.AgentIdentity) error {
	if err := r.CheckPending(ctx, id, a); err != nil {
		return err
	}
	if !r.consumed.CompareAndSwap(false, true) {
		return workloadidentity.ErrIdentity
	}
	r.active.Store(true)
	return nil
}
func (r *runtimeAuthority) Observe(ctx context.Context, a domain.AgentIdentity, b domain.ObservationBatch) error {
	if err := r.CheckAgent(ctx, a); err != nil {
		return err
	}
	if b.Agent != a {
		return runtimecatalog.ErrUnauthorized
	}
	return nil
}
func (r *runtimeAuthority) Watch(ctx context.Context, a domain.AgentIdentity, _ runtimecatalog.WakeSource, send func(context.Context, *domain.Snapshot) error) error {
	if err := r.CheckAgent(ctx, a); err != nil {
		return err
	}
	now := time.Now()
	return send(ctx, &domain.Snapshot{Scope: a.Scope, DestinationID: a.DestinationID, Epoch: a.Epoch, Full: true, Revision: "fixture", IssuedAt: now, ExpiresAt: now.Add(20 * time.Second)})
}

type noWake struct{}

func (noWake) Watch(string) (wake <-chan struct{}, unsubscribe func()) { return nil, func() {} }

// qualifyRuntime joins the real SPIRE sources to both ends of the HTTP protocol.
// Enrollment state is deliberately in memory here; PostgreSQL acceptance is separate.
func qualifyRuntime(ctx context.Context, conn grpc.ClientConnInterface, issuer workloadidentity.EnrollmentIssuer, adminSocket string, source *workloadidentity.Source, binding workloadidentity.Binding, socket string) (err error) {
	controlIssuer, err := workloadidentity.NewSPIREIssuer(conn, "loomplane.test")
	if err != nil {
		return err
	}
	// Retrying the same durable registration ID must not broaden its selectors.
	entry, err := issuer.Register(ctx, binding)
	if err != nil {
		return fmt.Errorf("retry existing SPIRE registration: %w", err)
	}
	controlURI, err := workloadidentity.ControlURI(binding.Agent)
	if err != nil {
		return err
	}
	const controlEntryID = "21234567-89ab-cdef-0123-456789abcdef"
	controlEntry := &spiretypes.Entry{Id: controlEntryID, SpiffeId: &spiretypes.SPIFFEID{TrustDomain: "loomplane.test", Path: "/loomplane/control/org/" + binding.Agent.OrgID}, ParentId: entry.ParentId, Selectors: entry.Selectors, X509SvidTtl: 30, JwtSvidTtl: 30}
	created, err := entryv1.NewEntryClient(conn).BatchCreateEntry(ctx, &entryv1.BatchCreateEntryRequest{Entries: []*spiretypes.Entry{controlEntry}})
	if err != nil {
		return err
	}
	if created == nil || len(created.Results) != 1 || created.Results[0] == nil || created.Results[0].Status == nil || created.Results[0].Status.Code != int32(codes.OK) {
		return errors.New("control fixture registration failed")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = errors.Join(err, controlIssuer.Revoke(cleanup, controlEntryID))
	}()
	control, err := readySource(ctx, socket, controlURI)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, control.Close()) }()
	authority := &runtimeAuthority{identity: binding.Agent}
	handler, err := runtimeapi.NewHandler(binding.Agent.OrgID, control, authority, authority, authority, noWake{}, authority)
	if err != nil {
		return err
	}
	endpoint, closeServer, err := startRuntime(ctx, handler, control)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeServer()) }()
	client, err := agent.NewTransport(endpoint, controlURI, source, binding.Agent)
	if err != nil {
		return err
	}
	defer client.Close()
	batch := domain.ObservationBatch{Agent: binding.Agent, DestinationGeneration: binding.Destination.Generation, Sequence: 1, Full: true}
	if deniedErr := client.Publish(ctx, batch); !errors.Is(deniedErr, agent.ErrClosed) {
		return errors.Join(errors.New("pending observer was not denied"), deniedErr)
	}
	if enrollErr := client.Enroll(ctx, fixtureEnrollmentID); enrollErr != nil {
		return enrollErr
	}
	if deniedErr := client.Enroll(ctx, fixtureEnrollmentID); !errors.Is(deniedErr, agent.ErrClosed) {
		return errors.Join(errors.New("enrollment replay was not denied"), deniedErr)
	}
	if publishErr := client.Publish(ctx, batch); publishErr != nil {
		return publishErr
	}
	stop := errors.New("fixture received complete snapshot")
	count := 0
	err = client.Watch(ctx, func(context.Context, domain.Snapshot) error { count++; return stop })
	if !errors.Is(err, stop) || count != 1 {
		return fmt.Errorf("runtime watch: count=%d: %w", count, err)
	}
	err = nil
	authority.active.Store(false)
	if deniedErr := client.Publish(ctx, batch); !errors.Is(deniedErr, agent.ErrClosed) {
		return errors.Join(errors.New("revoked observer was not denied"), deniedErr)
	}
	fmt.Println("PASS real SPIRE mTLS agent enrollment, replay denial, observation, snapshot transport and revocation")
	return qualifyPersisted(ctx, issuer, binding, socket, adminSocket, control)
}

func readySource(ctx context.Context, socket, uri string) (*workloadidentity.Source, error) {
	var source *workloadidentity.Source
	var sourceErr error
	ready, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	err := until(ready, func() bool {
		source, sourceErr = workloadidentity.OpenSource(ready, socket, uri)
		return sourceErr == nil
	})
	if err != nil {
		return nil, fmt.Errorf("workload API propagation: %w", errors.Join(err, sourceErr))
	}
	return source, nil
}
func startRuntime(ctx context.Context, handler *runtimeapi.Handler, control *workloadidentity.Source) (endpoint string, cleanup func() error, err error) {
	config, err := control.ServerTLS(control)
	if err != nil {
		return "", nil, err
	}
	server, err := runtimeapi.NewServer("127.0.0.1:0", handler, config)
	if err != nil {
		return "", nil, err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	served := make(chan error, 1)
	go func() { served <- server.ServeTLS(listener, "", "") }()
	closeServer := func() error {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(cleanup)
		if shutdownErr != nil {
			shutdownErr = errors.Join(shutdownErr, server.Close())
		}
		serveErr := <-served
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(shutdownErr, serveErr)
	}
	return "https://" + listener.Addr().String(), closeServer, nil
}

// This identity fixture does not issue signed policy and cannot acknowledge it.
func (r *runtimeAuthority) AcknowledgeSnapshot(context.Context, domain.AgentIdentity, domain.Acknowledgment) error {
	return runtimecatalog.ErrUnauthorized
}

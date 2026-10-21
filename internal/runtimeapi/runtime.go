package runtimeapi

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
)

type runtimeSource interface {
	workloadidentity.TrustSource
	x509svid.Source
	ServerTLS(x509bundle.Source) (*tls.Config, error)
	Close() error
}

// Runtime owns the dedicated agent listener and its rotating SPIRE credentials.
// Construction performs no I/O; Start runs after database migrations.
type Runtime struct {
	cfg              config.FederationRuntimeConfig
	db               *pgdriver.PgDB
	wake             runtimecatalog.WakeSource
	openSource       func(context.Context, string, string) (runtimeSource, error)
	listen           func(context.Context, string) (net.Listener, error)
	lifecycle        sync.Mutex
	mu               sync.Mutex
	server           *http.Server
	source           runtimeSource
	keys             *access.SigningKeyring
	cancel           context.CancelFunc
	stopCancellation func() bool
	done             chan struct{}
	address          string
	running          bool
	serveErr         error
	closeErr         error
}

func NewRuntime(cfg config.FederationRuntimeConfig, db *pgdriver.PgDB, wake runtimecatalog.WakeSource) (*Runtime, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Enabled && (db == nil || wake == nil) {
		return nil, errors.New("federation runtime requires database and event bus")
	}
	return &Runtime{cfg: cfg, db: db, wake: wake, openSource: func(ctx context.Context, address, uri string) (runtimeSource, error) {
		return workloadidentity.OpenSource(ctx, address, uri)
	}, listen: func(ctx context.Context, address string) (net.Listener, error) {
		return (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	}}, nil
}

func (r *Runtime) Start(ctx context.Context) (err error) {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	if !r.cfg.Enabled {
		return nil
	}
	r.mu.Lock()
	alreadyStarted := r.server != nil
	r.mu.Unlock()
	if alreadyStarted {
		return errors.New("federation runtime already started")
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	keys, err := access.LoadSigningKeyring(r.cfg.SigningKeysFile, r.cfg.OrganizationID)
	if err != nil {
		return err
	}
	uri, err := workloadidentity.ControlURIForOrganization(r.cfg.TrustDomain, r.cfg.OrganizationID)
	if err != nil {
		return err
	}
	startup, stopStartup := context.WithTimeout(ctx, 30*time.Second)
	defer stopStartup()
	source, err := r.openSource(startup, r.cfg.WorkloadAPI, uri)
	if err != nil {
		return fmt.Errorf("federation runtime identity: %w", err)
	}
	transferred := false
	defer func() {
		if !transferred {
			err = errors.Join(err, source.Close())
		}
	}()
	svid, err := source.GetX509SVID()
	if err != nil || svid == nil || svid.ID.String() != uri || source.Domain() != r.cfg.TrustDomain {
		return workloadidentity.ErrIdentity
	}
	registry := runtimecatalog.NewPgStore(r.db)
	policies := access.NewPolicyStore(r.db)
	distributor, err := access.NewDistributor(policies, registry, keys.Signer)
	if err != nil {
		return err
	}
	handler, err := NewHandler(r.cfg.OrganizationID, source, registry, workloadidentity.NewEnrollmentStore(r.db, r.cfg.TrustDomain), distributor, r.wake, policies)
	if err != nil {
		return err
	}
	tlsConfig, err := source.ServerTLS(source)
	if err != nil {
		return err
	}
	server, err := NewServer(r.cfg.ListenAddress, handler, tlsConfig)
	if err != nil {
		return err
	}
	listener, err := r.listen(startup, r.cfg.ListenAddress)
	if err != nil {
		return fmt.Errorf("federation runtime listener: %w", err)
	}
	if contextErr := startup.Err(); contextErr != nil {
		return errors.Join(contextErr, listener.Close())
	}
	runtimeContext, cancel := context.WithCancel(ctx)
	server.BaseContext = func(net.Listener) context.Context { return runtimeContext }
	r.mu.Lock()
	r.server, r.source, r.keys = server, source, keys
	r.address = listener.Addr().String()
	r.cancel = cancel
	r.done = make(chan struct{})
	r.running = true
	r.stopCancellation = context.AfterFunc(runtimeContext, func() { _ = server.Close() })
	r.mu.Unlock()
	transferred = true
	go func() {
		serveErr := server.ServeTLS(listener, "", "")
		// TLS setup can fail before net/http takes ownership of the listener.
		_ = listener.Close()
		r.mu.Lock()
		defer r.mu.Unlock()
		r.running = false
		if !errors.Is(serveErr, http.ErrServerClosed) {
			r.serveErr = serveErr
		}
		r.stopCancellation()
		cancel()
		// Serve can fail while existing streams are active. Cancel their contexts
		// and close connections before releasing identity material.
		_ = server.Close()
		r.closeErr = source.Close()
		close(r.done)
	}()
	return nil
}

func (r *Runtime) Stop(ctx context.Context) error {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	r.mu.Lock()
	server, done := r.server, r.done
	if server == nil {
		r.mu.Unlock()
		return nil
	}
	select {
	case <-done:
		err := r.closeErr
		r.mu.Unlock()
		return err
	default:
	}
	r.running = false
	r.stopCancellation()
	r.cancel()
	r.mu.Unlock()
	shutdownErr := server.Shutdown(ctx)
	if shutdownErr != nil {
		_ = server.Close()
	}
	select {
	case <-done:
		r.mu.Lock()
		defer r.mu.Unlock()
		return errors.Join(shutdownErr, r.closeErr)
	case <-ctx.Done():
		return errors.Join(shutdownErr, ctx.Err())
	}
}

func (r *Runtime) Health(ctx context.Context) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if !r.cfg.Enabled {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.running {
		return errors.Join(errors.New("federation runtime is not serving"), r.serveErr, r.closeErr)
	}
	if _, err := r.source.GetX509SVID(); err != nil {
		return err
	}
	return r.keys.Health(ctx)
}

// Address reports the bound endpoint for local diagnostics and acceptance tests.
func (r *Runtime) Address() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.address
}

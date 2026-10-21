package runtimeapi

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type lifecycleSource struct {
	fixedSVID
	*workloadidentity.Trust
	closed      atomic.Int32
	unhealthy   atomic.Bool
	tlsOverride *tls.Config
}

func (s *lifecycleSource) ServerTLS(bundle x509bundle.Source) (*tls.Config, error) {
	if s.tlsOverride != nil {
		return s.tlsOverride, nil
	}
	cfg := tlsconfig.MTLSServerConfig(s, bundle, tlsconfig.AuthorizeAny())
	cfg.MinVersion = tls.VersionTLS13
	cfg.SessionTicketsDisabled = true
	return cfg, nil
}
func (s *lifecycleSource) GetX509SVID() (*x509svid.SVID, error) {
	if s.unhealthy.Load() {
		return nil, workloadidentity.ErrIdentity
	}
	return s.fixedSVID.GetX509SVID()
}
func (s *lifecycleSource) Close() error { s.closed.Add(1); return nil }

func lifecycleFixture(t *testing.T) (*Runtime, *lifecycleSource) {
	t.Helper()
	identity := runtimeAgent(t)
	uri, err := workloadidentity.ControlURI(identity)
	if err != nil {
		t.Fatal(err)
	}
	trust, _, _ := runtimeCredentials(t, identity)
	_, controlCert, controlPair := runtimeCredentials(t, domain.AgentIdentity{URI: uri})
	id, err := spiffeid.FromString(uri)
	if err != nil {
		t.Fatal(err)
	}
	source := &lifecycleSource{fixedSVID: fixedSVID{&x509svid.SVID{ID: id, Certificates: []*x509.Certificate{controlCert}, PrivateKey: controlPair.PrivateKey.(crypto.Signer)}}, Trust: trust}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "keys.json")
	body, err := json.Marshal(map[string]any{"version": 1, "keys": []any{map[string]any{"org_id": identity.OrgID, "project_id": identity.ProjectID, "environment_id": identity.EnvironmentID, "private_key": base64.StdEncoding.EncodeToString(private), "not_before": time.Now().Add(-time.Minute), "not_after": time.Now().Add(time.Hour)}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.FederationRuntimeConfig{Enabled: true, ListenAddress: "127.0.0.1:9443", OrganizationID: identity.OrgID, TrustDomain: id.TrustDomain().Name(), WorkloadAPI: "unix:///run/spire/agent.sock", SigningKeysFile: file}
	runtime, err := NewRuntime(cfg, pgdriver.New(), inertWake{})
	if err != nil {
		t.Fatal(err)
	}
	runtime.openSource = func(ctx context.Context, address, expected string) (runtimeSource, error) {
		if address != cfg.WorkloadAPI || expected != uri {
			t.Fatal("runtime changed pinned identity")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded identity startup")
		}
		return source, nil
	}
	runtime.listen = func(ctx context.Context, address string) (net.Listener, error) {
		if address != cfg.ListenAddress {
			t.Fatal("runtime changed bind address")
		}
		return (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := runtime.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return runtime, source
}

func TestRuntimeDisabledDoesNotAcquireResources(t *testing.T) {
	runtime, err := NewRuntime(config.FederationRuntimeConfig{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime.openSource = func(context.Context, string, string) (runtimeSource, error) {
		t.Fatal("disabled runtime opened identity")
		return nil, nil
	}
	runtime.listen = func(context.Context, string) (net.Listener, error) {
		t.Fatal("disabled runtime opened listener")
		return nil, nil
	}
	if err := runtime.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Health(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeLifecycleAndHealth(t *testing.T) {
	runtime, source := lifecycleFixture(t)
	if err := runtime.Health(t.Context()); err == nil {
		t.Fatal("unstarted runtime healthy")
	}
	if err := runtime.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(t.Context()); err == nil {
		t.Fatal("duplicate startup accepted")
	}
	if err := runtime.Health(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A plaintext request must never reach the runtime handler.
	client := &http.Client{Timeout: time.Second}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+runtime.Address()+"/runtime/v1/watch", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err == nil {
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatal("plaintext reached runtime handler", response.StatusCode)
		}
	}
	source.unhealthy.Store(true)
	if err := runtime.Health(t.Context()); err == nil {
		t.Fatal("invalid workload identity healthy")
	}
	source.unhealthy.Store(false)
	if err := runtime.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if source.closed.Load() != 1 {
		t.Fatal("identity source not closed exactly once")
	}
	if err := runtime.Health(t.Context()); err == nil {
		t.Fatal("stopped runtime healthy")
	}
	if err := runtime.Stop(t.Context()); err != nil || source.closed.Load() != 1 {
		t.Fatal("stop not idempotent", err)
	}
}

func TestRuntimeStartupFailureClosesIdentity(t *testing.T) {
	runtime, source := lifecycleFixture(t)
	denied := errors.New("address unavailable")
	runtime.listen = func(context.Context, string) (net.Listener, error) { return nil, denied }
	if err := runtime.Start(t.Context()); !errors.Is(err, denied) {
		t.Fatal("bind failure hidden", err)
	}
	if source.closed.Load() != 1 {
		t.Fatal("bind failure leaked identity source")
	}
}

func TestRuntimeMissingKeysDoesNotOpenIdentity(t *testing.T) {
	runtime, _ := lifecycleFixture(t)
	runtime.cfg.SigningKeysFile = filepath.Join(t.TempDir(), "missing")
	runtime.openSource = func(context.Context, string, string) (runtimeSource, error) {
		t.Fatal("identity opened before valid keys")
		return nil, nil
	}
	if err := runtime.Start(t.Context()); err == nil {
		t.Fatal("missing keys accepted")
	}
}

func TestRuntimeListenerFailureAndParentCancellation(t *testing.T) {
	for _, parentCanceled := range []bool{false, true} {
		t.Run(map[bool]string{true: "parent", false: "listener"}[parentCanceled], func(t *testing.T) {
			runtime, source := lifecycleFixture(t)
			var listener net.Listener
			runtime.listen = func(ctx context.Context, _ string) (net.Listener, error) {
				var err error
				listener, err = (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
				return listener, err
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if err := runtime.Start(ctx); err != nil {
				t.Fatal(err)
			}
			if parentCanceled {
				cancel()
			} else if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-runtime.done:
			case <-time.After(2 * time.Second):
				t.Fatal("runtime failed to stop")
			}
			if err := runtime.Health(t.Context()); err == nil {
				t.Fatal("dead listener healthy")
			}
			if source.closed.Load() != 1 {
				t.Fatal("dead listener leaked identity")
			}
		})
	}
}

func TestRuntimeShutdownClosesStalledHandshake(t *testing.T) {
	runtime, source := lifecycleFixture(t)
	if err := runtime.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	client, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", runtime.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	// Partial TLS input leaves an accepted connection waiting for its handshake.
	if _, writeErr := client.Write([]byte{0x16}); writeErr != nil {
		t.Fatal(writeErr)
	}
	shutdown, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err = runtime.Stop(shutdown)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	select {
	case <-runtime.done:
	case <-time.After(time.Second):
		t.Fatal("shutdown leaked listener")
	}
	if source.closed.Load() != 1 {
		t.Fatal("shutdown leaked source")
	}
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var one [1]byte
	if _, err := client.Read(one[:]); err == nil {
		t.Fatal("stalled client still open")
	}
}

func TestRuntimeRejectsWrongIdentityAndUnsafeTLS(t *testing.T) {
	for _, unsafeTLS := range []bool{false, true} {
		t.Run(map[bool]string{true: "tls", false: "identity"}[unsafeTLS], func(t *testing.T) {
			runtime, source := lifecycleFixture(t)
			if unsafeTLS {
				source.tlsOverride = &tls.Config{MinVersion: tls.VersionTLS13}
			} else {
				id, err := spiffeid.FromString("spiffe://runtime.test/loomplane/control/org/other")
				if err != nil {
					t.Fatal(err)
				}
				source.value.ID = id
			}
			runtime.listen = func(context.Context, string) (net.Listener, error) {
				t.Fatal("unsafe runtime opened listener")
				return nil, nil
			}
			if err := runtime.Start(t.Context()); err == nil {
				t.Fatal("unsafe runtime started")
			}
			if source.closed.Load() != 1 {
				t.Fatal("failed startup leaked source")
			}
		})
	}
}

func TestRuntimeTLSStartupFailureReleasesListener(t *testing.T) {
	runtime, source := lifecycleFixture(t)
	source.tlsOverride = &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAnyClientCert, SessionTicketsDisabled: true}
	if err := runtime.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runtime.done:
	case <-time.After(time.Second):
		t.Fatal("TLS startup failure was not reported")
	}
	if err := runtime.Health(t.Context()); err == nil {
		t.Fatal("failed TLS listener healthy")
	}
	client, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", runtime.Address())
	if err == nil {
		client.Close()
		t.Fatal("TLS startup failure left listening socket open")
	}
}

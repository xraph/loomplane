package agent

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type launchSourceFixture struct {
	CredentialSource
	closed     atomic.Int32
	closeError error
}

func (s *launchSourceFixture) Close() error { s.closed.Add(1); return s.closeError }

type launchTransportFixture struct {
	*sessionClient
	closed  atomic.Int32
	publish func(context.Context, domain.ObservationBatch) error
}

func (c *launchTransportFixture) Close() { c.closed.Add(1) }
func (c *launchTransportFixture) Publish(ctx context.Context, batch domain.ObservationBatch) error {
	if c.publish != nil {
		return c.publish(ctx, batch)
	}
	return nil
}

func TestLauncherAppliesSignedPolicyAndClosesResources(t *testing.T) {
	h := newReceiverHarness(t)
	cfg := launchFixture(t)
	cfg.Registration.Agent = h.identity
	cfg.SigningKeys = []access.SnapshotKey{h.signer.Key()}
	source := &launchSourceFixture{}
	client := &launchTransportFixture{sessionClient: &sessionClient{}}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	client.watch = func(ctx context.Context, receive func(context.Context, domain.Snapshot) error) error {
		return receive(ctx, h.snapshot(t, 1))
	}
	client.acknowledge = func(_ context.Context, ack domain.Acknowledgment) error {
		if ack.Agent != h.identity || ack.Sequence != 1 || ack.Revision != "routes" {
			t.Fatal("wrong applied receipt")
		}
		cancel()
		return nil
	}
	deps := launchDependencies{
		openSource: func(ctx context.Context, address, uri string) (launcherSource, error) {
			if uri != h.identity.URI || address != cfg.WorkloadAPI {
				t.Fatal("identity changed")
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("unbounded identity startup")
			}
			return source, nil
		},
		transport: func(endpoint, control string, _ CredentialSource, identity domain.AgentIdentity) (launcherTransport, error) {
			if endpoint != cfg.ControlEndpoint || control != "spiffe://loomplane.test/loomplane/control/org/org" || identity != h.identity {
				t.Fatal("transport scope changed")
			}
			return client, nil
		},
		slices: func(string) (runtimecatalog.EndpointSliceClient, func(), error) {
			t.Fatal("consumer-only launcher acquired Kubernetes credentials")
			return nil, nil, nil
		},
	}
	if err := launch(ctx, cfg, deps); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if client.enrolls.Load() != 1 || client.acknowledgments.Load() != 1 || source.closed.Load() != 1 || client.closed.Load() != 1 {
		t.Fatal("launcher skipped enrollment/application or leaked resources")
	}
	restored, err := OpenStore(cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err := restored.NextSequence(t.Context()); !errors.Is(err, ErrClosed) {
		t.Fatal("restart inherited authority", err)
	}
	if err := restored.Activate(h.identity); !errors.Is(err, ErrClosed) {
		t.Fatal("restart reused persisted epoch", err)
	}
}
func TestLauncherSetupFailureDoesNotEnroll(t *testing.T) {
	cfg := launchFixture(t)
	source := &launchSourceFixture{}
	failure := errors.New("transport unavailable")
	deps := launchDependencies{openSource: func(context.Context, string, string) (launcherSource, error) { return source, nil }, transport: func(string, string, CredentialSource, domain.AgentIdentity) (launcherTransport, error) {
		return nil, failure
	}}
	if err := launch(t.Context(), cfg, deps); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if source.closed.Load() != 1 {
		t.Fatal("setup leaked source")
	}
}
func TestLauncherRejectsInvalidConfigurationBeforeIO(t *testing.T) {
	cfg := launchFixture(t)
	cfg.Version = 0
	if err := launch(t.Context(), cfg, launchDependencies{}); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
}
func TestAgentCommandHelpDoesNotLoadConfiguration(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--version"}} {
		var output bytes.Buffer
		if err := RunCommand(t.Context(), args, &output, "test-version"); err != nil {
			t.Fatal(err)
		}
		if output.Len() == 0 {
			t.Fatal("missing command output")
		}
	}
	var output bytes.Buffer
	if err := RunCommand(t.Context(), nil, &output, "test-version"); err == nil {
		t.Fatal("missing config accepted")
	}
	if err := RunCommand(t.Context(), []string{"--config", "missing", "extra"}, &output, "test-version"); err == nil || strings.Contains(err.Error(), "private_key") {
		t.Fatal("unexpected argument accepted", err)
	}
}

type launchSliceFixture struct{ item discoveryv1.EndpointSlice }

func (s launchSliceFixture) List(_ context.Context, options metav1.ListOptions) (*discoveryv1.EndpointSliceList, error) {
	if options.LabelSelector != "kubernetes.io/service-name in (api)" {
		return nil, errors.New("missing export selector")
	}
	return &discoveryv1.EndpointSliceList{ListMeta: metav1.ListMeta{ResourceVersion: "1"}, Items: []discoveryv1.EndpointSlice{s.item}}, nil
}
func (s launchSliceFixture) Watch(context.Context, metav1.ListOptions) (watch.Interface, error) {
	return watch.NewRaceFreeFake(), nil
}
func TestLauncherPublishesOnlyConfiguredKubernetesService(t *testing.T) {
	cfg := launchFixture(t)
	cfg.Registration.Exports = map[string]runtimecatalog.ExportBounds{"export": {Scope: cfg.Registration.Agent.Scope, DestinationID: cfg.Registration.Agent.DestinationID, PlacementID: "placement", BindingID: "binding", Port: 8080, CIDRs: []string{"10.0.0.0/24"}}}
	cfg.Targets = []runtimecatalog.KubernetesTarget{{ExportID: "export", ServiceName: "api", ServiceUID: "service-uid", PortName: "http"}}
	name, port, controller := "http", int32(8080), true
	slices := launchSliceFixture{item: discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Namespace: cfg.Namespace, Name: "slice", UID: "slice-uid", ResourceVersion: "1", Labels: map[string]string{discoveryv1.LabelServiceName: "api"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: "api", UID: "service-uid", Controller: &controller}}}, AddressType: discoveryv1.AddressTypeIPv4, Ports: []discoveryv1.EndpointPort{{Name: &name, Port: &port}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.2"}}}}}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	client := &launchTransportFixture{sessionClient: &sessionClient{}}
	observed := false
	client.publish = func(_ context.Context, batch domain.ObservationBatch) error {
		if batch.Agent != cfg.Registration.Agent || batch.Sequence != 1 || !batch.Full || len(batch.Endpoints) != 1 || batch.Endpoints[0].ExportID != "export" || batch.Endpoints[0].Address != "10.0.0.2" {
			return errors.New("configured service was not published")
		}
		observed = true
		cancel()
		return nil
	}
	closed := false
	source := &launchSourceFixture{}
	deps := launchDependencies{openSource: func(context.Context, string, string) (launcherSource, error) { return source, nil }, transport: func(string, string, CredentialSource, domain.AgentIdentity) (launcherTransport, error) {
		return client, nil
	}, slices: func(namespace string) (runtimecatalog.EndpointSliceClient, func(), error) {
		if namespace != cfg.Namespace {
			return nil, nil, ErrConfiguration
		}
		return slices, func() { closed = true }, nil
	}}
	if err := launch(ctx, cfg, deps); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !observed || !closed || client.enrolls.Load() != 1 {
		t.Fatal("configured observer did not run or close")
	}
}

func TestLauncherDoesNotHideCleanupFailureOnCancellation(t *testing.T) {
	cfg := launchFixture(t)
	failure := errors.New("source close failed")
	source := &launchSourceFixture{closeError: failure}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client := &launchTransportFixture{sessionClient: &sessionClient{}}
	client.watch = func(context.Context, func(context.Context, domain.Snapshot) error) error {
		cancel()
		return context.Canceled
	}
	deps := launchDependencies{openSource: func(context.Context, string, string) (launcherSource, error) { return source, nil }, transport: func(string, string, CredentialSource, domain.AgentIdentity) (launcherTransport, error) {
		return client, nil
	}}
	err := launch(ctx, cfg, deps)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, failure) || !errors.Is(err, ErrCleanup) {
		t.Fatal("cleanup failure looks like successful cancellation", err)
	}
}

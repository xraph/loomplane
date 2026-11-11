package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xraph/relay"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/endpoint"
	relayid "github.com/xraph/relay/id"
	"github.com/xraph/relay/signature"
	relaymem "github.com/xraph/relay/store/memory"

	"github.com/xraph/loomplane/pkg/fleet"
)

func fleetDeliveryFixture(t *testing.T) (*Service, *endpoint.Endpoint) {
	t.Helper()
	raw := relaymem.New()
	wrapped, err := newRelayStore(raw, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := relay.New(relay.WithStore(wrapped))
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{relay: r, relayStore: wrapped}
	s.cfg.Identity.AppID = "app"
	if err = s.SetFleetDestinations([]FleetDestination{{ID: "receiver", OrgID: "org", FleetID: "fleet", URL: "https://hooks.example.com/fleet", Revision: 1}}); err != nil {
		t.Fatal(err)
	}
	if err = s.registerFleetEventTypes(t.Context()); err != nil {
		t.Fatal(err)
	}
	ep := &endpoint.Endpoint{ID: relayid.NewEndpointID(), TenantID: fleetTenant("org", "fleet"), ScopeAppID: "app", ScopeOrgID: "org", URL: "https://hooks.example.com/fleet", Secret: "synthetic-signing-key", Enabled: true, RateLimit: 5, EventTypes: []string{"fleet.*"}, Metadata: map[string]string{"fleet_id": "fleet", "destination_id": "receiver", "destination_revision": "1"}}
	if err = wrapped.CreateEndpoint(t.Context(), ep); err != nil {
		t.Fatal(err)
	}
	return s, ep
}
func TestFleetDeliveryDuplicate(t *testing.T) {
	s, ep := fleetDeliveryFixture(t)
	e := fleet.Event{Version: 1, OrgID: "org", ID: "event", FleetID: "fleet", InstanceID: "instance", Sequence: 1, Generation: 1, Kind: "lifecycle", OccurredAt: time.Now(), Lifecycle: &fleet.LifecycleEvent{State: "ready"}}
	if err := s.DeliverFleetEvent(t.Context(), "org", e); err != nil {
		t.Fatal(err)
	}
	if err := s.DeliverFleetEvent(t.Context(), "org", e); err != nil {
		t.Fatal(err)
	}
	items, err := s.relayStore.ListByEndpoint(t.Context(), ep.ID, delivery.ListOpts{Limit: 10})
	if err != nil || len(items) != 1 {
		t.Fatalf("durable fanout: %+v %v", items, err)
	}
	e.Lifecycle = &fleet.LifecycleEvent{State: "failed"}
	if err = s.DeliverFleetEvent(t.Context(), "org", e); err == nil {
		t.Fatal("same event ID accepted with changed body")
	}
}
func TestFleetDeliveryRevokedEndpoint(t *testing.T) {
	s, ep := fleetDeliveryFixture(t)
	if _, err := s.relayStore.GetEndpoint(t.Context(), ep.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFleetDestinations(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.relayStore.GetEndpoint(t.Context(), ep.ID); err == nil {
		t.Fatal("revoked operator destination still usable")
	}
	if err := s.SetFleetDestinations([]FleetDestination{{ID: "receiver", OrgID: "org", FleetID: "fleet", URL: ep.URL, Revision: 1}}); err != nil {
		t.Fatal(err)
	}
	ep.Enabled = false
	if err := s.relayStore.UpdateEndpoint(t.Context(), ep); err != nil {
		t.Fatal(err)
	}
	if _, err := s.relayStore.GetEndpoint(t.Context(), ep.ID); err == nil {
		t.Fatal("disabled endpoint still usable")
	}
}
func TestFleetDeliveryEndpointRotation(t *testing.T) {
	s, ep := fleetDeliveryFixture(t)
	ep.Secret = "rotated-synthetic-key"
	if err := s.relayStore.UpdateEndpoint(t.Context(), ep); err != nil {
		t.Fatal(err)
	}
	got, err := s.relayStore.GetEndpoint(t.Context(), ep.ID)
	if err != nil || got.Secret != ep.Secret {
		t.Fatalf("key rotation: %+v %v", got, err)
	}
	if err = s.SetFleetDestinations([]FleetDestination{{ID: "receiver", OrgID: "org", FleetID: "fleet", URL: "https://hooks.example.com/new", Revision: 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.relayStore.GetEndpoint(t.Context(), ep.ID); err == nil {
		t.Fatal("old destination survived policy rotation")
	}
}
func TestFleetDeliveryUnsafePolicy(t *testing.T) {
	s, _ := fleetDeliveryFixture(t)
	for _, url := range []string{"http://hooks.example.com/", "https://127.0.0.1/", "https://user:secret@hooks.example.com/", "https://hooks.example.com/?secret=value"} {
		if err := s.SetFleetDestinations([]FleetDestination{{ID: "receiver", OrgID: "org", FleetID: "fleet", URL: url, Revision: 2}}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unsafe policy accepted: %s %v", url, err)
		}
	}
}

func TestFleetDeliveryResponseRedaction(t *testing.T) {
	s, ep := fleetDeliveryFixture(t)
	e := fleet.Event{Version: 1, OrgID: "org", ID: "event", FleetID: "fleet", InstanceID: "instance", Sequence: 1, Generation: 1, Kind: "lifecycle", OccurredAt: time.Now(), Lifecycle: &fleet.LifecycleEvent{State: "ready"}}
	if err := s.DeliverFleetEvent(t.Context(), "org", e); err != nil {
		t.Fatal(err)
	}
	items, err := s.relayStore.ListByEndpoint(t.Context(), ep.ID, delivery.ListOpts{Limit: 10})
	if err != nil || len(items) != 1 {
		t.Fatalf("fanout: %v", err)
	}
	d := items[0]
	d.LastResponse = "synthetic-secret"
	d.LastError = "token=synthetic-secret"
	d.State = delivery.StateFailed
	if err = s.relayStore.UpdateDelivery(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	raw, err := s.relayStore.GetDelivery(t.Context(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if raw.LastResponse != "" || raw.LastError != "delivery failed" {
		t.Fatalf("receiver data retained: %+v", raw)
	}
	if d.LastResponse != "synthetic-secret" {
		t.Fatal("redaction mutated caller's value")
	}
}

type fleetCallbackCapture struct {
	body    []byte
	headers http.Header
}
type fleetCallbackTransport struct{ calls chan fleetCallbackCapture }

func (r fleetCallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	r.calls <- fleetCallbackCapture{body: body, headers: req.Header.Clone()}
	return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("synthetic-response-secret")), Request: req}, nil
}
func TestFleetDeliverySignedRetryAndDeadLetter(t *testing.T) {
	s, ep := fleetDeliveryFixture(t)
	captures := make(chan fleetCallbackCapture, 4)
	engine, err := relay.New(relay.WithStore(s.relayStore), relay.WithHTTPClient(&http.Client{Transport: fleetCallbackTransport{calls: captures}, Timeout: time.Second}), relay.WithConcurrency(1), relay.WithPollInterval(time.Millisecond), relay.WithMaxRetries(2), relay.WithRetrySchedule([]time.Duration{time.Millisecond}))
	if err != nil {
		t.Fatal(err)
	}
	s.relay = engine
	e := fleet.Event{Version: 1, OrgID: "org", ID: "stable-event", FleetID: "fleet", InstanceID: "instance", Sequence: 1, Generation: 1, Kind: "lifecycle", OccurredAt: time.Now(), Lifecycle: &fleet.LifecycleEvent{State: "ready"}}
	if err = s.DeliverFleetEvent(t.Context(), "org", e); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	engine.Start(ctx)
	defer engine.Stop(context.Background())
	for range 2 {
		select {
		case capture := <-captures:
			stamp, parseErr := strconv.ParseInt(capture.headers.Get(delivery.HeaderTimestamp), 10, 64)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			sig := capture.headers.Get(delivery.HeaderSignature)
			if !signature.Verify(capture.body, ep.Secret, stamp, sig) || signature.Verify(append(capture.body, ' '), ep.Secret, stamp, sig) || signature.Verify(capture.body, ep.Secret, stamp-1, sig) {
				t.Fatal("body and timestamp not bound to signature")
			}
			if !strings.Contains(string(capture.body), `"id":"stable-event"`) {
				t.Fatal("stable source event ID absent from signed body")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("bounded retry did not run")
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		items, listErr := s.relayStore.ListByEndpoint(t.Context(), ep.ID, delivery.ListOpts{Limit: 10})
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(items) == 1 && items[0].State == delivery.StateFailed {
			if items[0].AttemptCount != 2 || items[0].LastResponse != "" {
				t.Fatalf("failed delivery: %+v", items[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retry limit did not produce a dead letter")
		}
		time.Sleep(time.Millisecond * 10)
	}
}

func TestFleetDeliveryOperatorConfiguration(t *testing.T) {
	s, _ := fleetDeliveryFixture(t)
	path := filepath.Join(t.TempDir(), "destinations.json")
	for _, body := range []string{`null`, `[{"id":"receiver","org_id":"org","fleet_id":"fleet","url":"https://hooks.example.com/fleet","revision":1,"unexpected":true}]`, `[] {}`, strings.Repeat(" ", 256*1024+1)} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := s.loadFleetDestinations(path); err == nil {
			t.Fatal("invalid operator configuration accepted")
		}
	}
	if err := os.WriteFile(path, []byte(`[{"id":"receiver","org_id":"org","fleet_id":"fleet","url":"https://hooks.example.com/fleet","revision":1}]`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.loadFleetDestinations(path); err != nil {
		t.Fatal(err)
	}
	if err := s.loadFleetDestinations(""); err != nil {
		t.Fatal(err)
	}
}

func TestFleetDeliveryHistoryPaginationAndScope(t *testing.T) {
	s, ep := fleetDeliveryFixture(t)
	for n := range 55 {
		e := fleet.Event{Version: 1, ID: fmt.Sprintf("event-%02d", n), OrgID: "org", FleetID: "fleet", InstanceID: "instance", Generation: 1, Sequence: int64(n + 1), Kind: "lifecycle", OccurredAt: time.Now(), Lifecycle: &fleet.LifecycleEvent{State: "ready"}}
		if err := s.DeliverFleetEvent(t.Context(), "org", e); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.fleetDeliveryPage(t.Context(), "org", "fleet", fleet.CallbackQuery{Limit: 50})
	if err != nil || len(first.Items) != 50 || first.NextCursor == "" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	second, err := s.fleetDeliveryPage(t.Context(), "org", "fleet", fleet.CallbackQuery{Limit: 50, After: first.NextCursor})
	if err != nil || len(second.Items) != 5 || second.Items[0].InstanceID != "instance" || second.Items[0].EventID == "" {
		t.Fatalf("older outcomes: %+v %v", second, err)
	}
	if _, err = s.fleetDeliveryPage(t.Context(), "org", "other", fleet.CallbackQuery{EndpointID: ep.ID.String()}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign endpoint: %v", err)
	}
	empty, err := s.fleetDeliveryPage(t.Context(), "org", "other", fleet.CallbackQuery{})
	if err != nil || len(empty.Items) != 0 {
		t.Fatalf("foreign history disclosed: %+v %v", empty, err)
	}
}

func TestFleetDeliveryRevokedAfterAcceptance(t *testing.T) {
	s, ep := fleetDeliveryFixture(t)
	event := fleet.Event{Version: 1, OrgID: "org", ID: "event", FleetID: "fleet", InstanceID: "instance", Generation: 1, Sequence: 1, Kind: "lifecycle", OccurredAt: time.Now(), Lifecycle: &fleet.LifecycleEvent{State: "ready"}}
	if err := s.DeliverFleetEvent(t.Context(), "org", event); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFleetDestinations(nil); err != nil {
		t.Fatal(err)
	}
	calls := make(chan fleetCallbackCapture, 1)
	engine, err := relay.New(relay.WithStore(s.relayStore), relay.WithHTTPClient(&http.Client{Transport: fleetCallbackTransport{calls: calls}, Timeout: time.Second}), relay.WithConcurrency(1), relay.WithPollInterval(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	engine.Start(ctx)
	defer engine.Stop(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows, listErr := s.relayStore.ListByEndpoint(t.Context(), ep.ID, delivery.ListOpts{Limit: 10})
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(rows) == 1 && rows[0].State == delivery.StateFailed {
			if len(calls) != 0 {
				t.Fatal("revoked callback sent")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("revoked endpoint did not terminate delivery")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFleetDeliveryExactIntegersThroughRelay(t *testing.T) {
	s, ep := fleetDeliveryFixture(t)
	captures := make(chan fleetCallbackCapture, 1)
	engine, err := relay.New(relay.WithStore(s.relayStore), relay.WithHTTPClient(&http.Client{Transport: fleetCallbackTransport{calls: captures}, Timeout: time.Second}), relay.WithConcurrency(1), relay.WithPollInterval(time.Millisecond), relay.WithMaxRetries(1))
	if err != nil {
		t.Fatal(err)
	}
	s.relay = engine
	event := fleet.Event{Version: 1, OrgID: "org", ID: "event-9007199254740993", FleetID: "fleet", InstanceID: "instance", Sequence: math.MaxInt64, Generation: 9007199254740993, Kind: "scaling", OccurredAt: time.Now(), Scaling: &fleet.ScalingEvent{Service: "api", PolicyRevision: 9007199254740995, PreviousReplicas: 50, DesiredReplicas: 100, Reason: "manual_requested", Writer: "manual", ObservedAt: time.Now()}}
	if err = s.DeliverFleetEvent(t.Context(), "org", event); err != nil {
		t.Fatal(err)
	}
	rows, err := s.relayStore.ListByEndpoint(t.Context(), ep.ID, delivery.ListOpts{Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("fanout: %+v %v", rows, err)
	}
	persisted, err := s.relayStore.GetEvent(t.Context(), rows[0].EventID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(persisted.Data)
	if err != nil {
		t.Fatal(err)
	}
	assertExact := func(raw []byte) {
		t.Helper()
		var got fleet.Event
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.ID != event.ID || got.Sequence != event.Sequence || got.Generation != event.Generation || got.Scaling == nil || got.Scaling.PolicyRevision != event.Scaling.PolicyRevision || got.Scaling.PreviousReplicas != 50 || got.Scaling.DesiredReplicas != 100 {
			t.Fatalf("integer identity changed: %s", raw)
		}
	}
	assertExact(raw)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	engine.Start(ctx)
	defer engine.Stop(context.Background())
	select {
	case capture := <-captures:
		stamp, err := strconv.ParseInt(capture.headers.Get(delivery.HeaderTimestamp), 10, 64)
		if err != nil || !signature.Verify(capture.body, ep.Secret, stamp, capture.headers.Get(delivery.HeaderSignature)) {
			t.Fatal("invalid signed body")
		}
		assertExact(capture.body)
	case <-time.After(5 * time.Second):
		t.Fatal("callback not attempted")
	}
}

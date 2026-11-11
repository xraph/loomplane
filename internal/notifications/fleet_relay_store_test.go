//go:build integration

package notifications

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	relayid "github.com/xraph/relay/id"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/pkg/fleet"
)

func fleetRelayPGFixture(t *testing.T) (*Service, *endpoint.Endpoint) {
	t.Helper()
	db := notificationDatabase(t)
	svc, err := New(config.Config{SecretKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), Identity: config.IdentityConfig{AppID: "app"}}, db)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.relayStore.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = svc.SetFleetDestinations([]FleetDestination{{ID: "receiver", OrgID: "org", FleetID: "fleet", URL: "https://hooks.example.com/fleet", Revision: 1}}); err != nil {
		t.Fatal(err)
	}
	if err = svc.registerFleetEventTypes(t.Context()); err != nil {
		t.Fatal(err)
	}
	ep := &endpoint.Endpoint{ID: relayid.NewEndpointID(), TenantID: fleetTenant("org", "fleet"), ScopeAppID: "app", ScopeOrgID: "org", URL: "https://hooks.example.com/fleet", Secret: "synthetic-key", Enabled: true, RateLimit: 5, EventTypes: []string{"fleet.*"}, Metadata: map[string]string{"fleet_id": "fleet", "destination_id": "receiver", "destination_revision": "1"}}
	ep.CreatedAt = time.Now().UTC()
	ep.UpdatedAt = ep.CreatedAt
	if err = svc.relayStore.CreateEndpoint(t.Context(), ep); err != nil {
		t.Fatal(err)
	}
	if err = svc.DeliverFleetEvent(t.Context(), "org", fleet.Event{Version: 1, ID: "event", OrgID: "org", FleetID: "fleet", InstanceID: "instance", Sequence: 1, Generation: 1, Kind: "lifecycle", OccurredAt: time.Now(), Lifecycle: &fleet.LifecycleEvent{State: "ready"}}); err != nil {
		t.Fatal(err)
	}
	return svc, ep
}
func TestFleetDeliveryPostgresCrashRecoveryFenced(t *testing.T) {
	svc, ep := fleetRelayPGFixture(t)
	first, err := svc.relayStore.Dequeue(t.Context(), 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim: %+v %v", first, err)
	}
	if _, err = svc.pg.Exec(t.Context(), `UPDATE relay_deliveries SET updated_at=clock_timestamp()-interval '3 minutes' WHERE id=$1`, first[0].ID.String()); err != nil {
		t.Fatal(err)
	}
	// A fresh wrapper models a replacement worker after process loss.
	replacement, err := newRelayStore(svc.relayStore.Store, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	replacement.fleetPG = svc.pg
	recovered, err := replacement.Dequeue(t.Context(), 10)
	if err != nil || len(recovered) != 1 {
		t.Fatalf("recovery: %+v %v", recovered, err)
	}
	if recovered[0].EventID != first[0].EventID || recovered[0].ID != first[0].ID || recovered[0].AttemptCount != 1 {
		t.Fatalf("unstable retry: %+v", recovered[0])
	}
	first[0].State = delivery.StateDelivered
	if err = svc.relayStore.UpdateDelivery(t.Context(), first[0]); !errors.Is(err, ErrFleetDeliveryLease) {
		t.Fatalf("stale completion: %v", err)
	}
	recovered[0].State = delivery.StateDelivered
	recovered[0].AttemptCount++
	if err = replacement.UpdateDelivery(t.Context(), recovered[0]); err != nil {
		t.Fatal(err)
	}
	records, err := svc.relayStore.ListByEndpoint(t.Context(), ep.ID, delivery.ListOpts{Limit: 10})
	if err != nil || len(records) != 1 || records[0].State != delivery.StateDelivered {
		t.Fatalf("completion: %+v %v", records, err)
	}
}
func TestFleetDeliveryPostgresAmbiguousAttemptsBounded(t *testing.T) {
	svc, ep := fleetRelayPGFixture(t)
	if _, err := svc.pg.Exec(t.Context(), `UPDATE relay_deliveries SET max_attempts=2 WHERE endpoint_id=$1`, ep.ID.String()); err != nil {
		t.Fatal(err)
	}
	first, err := svc.relayStore.Dequeue(t.Context(), 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("first: %+v %v", first, err)
	}
	for round := 0; round < 2; round++ {
		if _, err = svc.pg.Exec(t.Context(), `UPDATE relay_deliveries SET updated_at=clock_timestamp()-interval '3 minutes' WHERE endpoint_id=$1`, ep.ID.String()); err != nil {
			t.Fatal(err)
		}
		claims, claimErr := svc.relayStore.Dequeue(t.Context(), 10)
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		if (round == 0 && len(claims) != 1) || (round == 1 && len(claims) != 0) {
			t.Fatalf("claims after loss %d: %d", round, len(claims))
		}
	}
	rows, err := svc.relayStore.ListByEndpoint(t.Context(), ep.ID, delivery.ListOpts{Limit: 10})
	if err != nil || rows[0].State != delivery.StateFailed || rows[0].AttemptCount != 2 {
		t.Fatalf("bounded recovery: %+v %v", rows, err)
	}
}

func TestFleetDeliveryRetentionClearsAllDetailedCopies(t *testing.T) {
	svc, ep := fleetRelayPGFixture(t)
	claims, err := svc.relayStore.Dequeue(t.Context(), 10)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim: %+v %v", claims, err)
	}
	d := claims[0]
	if err = svc.relayStore.RecordAttempt(t.Context(), &delivery.Attempt{ID: relayid.NewAttemptID(), DeliveryID: d.ID, AttemptNum: 1, Outcome: delivery.OutcomeRetry, AttemptedAt: time.Now(), Response: "synthetic-response"}); err != nil {
		t.Fatal(err)
	}
	event, err := svc.relayStore.GetEvent(t.Context(), d.EventID)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.relayStore.Push(t.Context(), &dlq.Entry{ID: relayid.NewDLQID(), DeliveryID: d.ID, EventID: d.EventID, EndpointID: ep.ID, TenantID: d.TenantID, EventType: d.EventType, Payload: event.Data, FailedAt: time.Now(), AttemptCount: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.pg.Exec(t.Context(), `UPDATE relay_events SET data=jsonb_set(data,'{occurred_at}',to_jsonb((clock_timestamp()-interval '31 days')::text)) WHERE id=$1`, d.EventID.String()); err != nil {
		t.Fatal(err)
	}
	if err = svc.pruneFleetRelayHistory(t.Context(), time.Now().Add(-30*24*time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	event, err = svc.relayStore.GetEvent(t.Context(), d.EventID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(event.Data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "lifecycle") || !strings.Contains(string(raw), `"id":"event"`) {
		t.Fatalf("retained event: %s", raw)
	}
	letters, err := svc.relayStore.ListDLQ(t.Context(), dlq.ListOpts{TenantID: d.TenantID, Limit: 10})
	if err != nil || len(letters) != 1 {
		t.Fatalf("letters: %+v %v", letters, err)
	}
	raw, err = json.Marshal(letters[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "lifecycle") {
		t.Fatalf("DLQ retained detail: %s", raw)
	}
	attempts, err := svc.relayStore.ListAttempts(t.Context(), d.ID)
	if err != nil || len(attempts) != 0 {
		t.Fatalf("retained attempts: %+v %v", attempts, err)
	}
	if err = svc.relayStore.RecordAttempt(t.Context(), &delivery.Attempt{ID: relayid.NewAttemptID(), DeliveryID: d.ID, AttemptNum: 1, Outcome: delivery.OutcomeRetry, AttemptedAt: time.Now()}); !errors.Is(err, ErrFleetDeliveryLease) {
		t.Fatalf("late attempt survived retention: %v", err)
	}
	if err = svc.relayStore.Push(t.Context(), &dlq.Entry{ID: relayid.NewDLQID(), DeliveryID: d.ID, EventID: d.EventID, EndpointID: ep.ID, TenantID: d.TenantID, EventType: d.EventType, Payload: map[string]any{"lifecycle": "stale-private-detail"}, FailedAt: time.Now(), AttemptCount: 1}); !errors.Is(err, ErrFleetDeliveryLease) {
		t.Fatalf("late dead letter survived retention: %v", err)
	}
	d.State = delivery.StateDelivered
	d.AttemptCount++
	if err = svc.relayStore.UpdateDelivery(t.Context(), d); !errors.Is(err, ErrFleetDeliveryLease) {
		t.Fatalf("expired worker resurrected event: %v", err)
	}
}

func TestFleetDeliveryRecoveryLeavesOrdinaryRows(t *testing.T) {
	svc, _ := fleetRelayPGFixture(t)
	queued, err := svc.relayStore.Dequeue(t.Context(), 10)
	if err != nil || len(queued) != 1 {
		t.Fatalf("queue: %+v %v", queued, err)
	}
	ordinary := *queued[0]
	ordinary.ID = relayid.NewDeliveryID()
	ordinary.TenantID = "org"
	ordinary.State = delivery.StatePending
	if err = svc.relayStore.Enqueue(t.Context(), &ordinary); err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.relayStore.Dequeue(t.Context(), 10)
	if err != nil || len(claimed) != 1 || claimed[0].ID != ordinary.ID {
		t.Fatalf("ordinary claim: %+v %v", claimed, err)
	}
	if _, err = svc.pg.Exec(t.Context(), `UPDATE relay_deliveries SET updated_at=clock_timestamp()-interval '3 minutes' WHERE id=$1`, ordinary.ID.String()); err != nil {
		t.Fatal(err)
	}
	recovered, err := svc.relayStore.Dequeue(t.Context(), 10)
	if err != nil || len(recovered) != 0 {
		t.Fatalf("ordinary behavior changed: %+v %v", recovered, err)
	}
	row, err := svc.relayStore.GetDelivery(t.Context(), ordinary.ID)
	if err != nil || row.State != delivery.StateDelivering || row.AttemptCount != 0 {
		t.Fatalf("ordinary state changed: %+v %v", row, err)
	}
}

package notifications

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xraph/relay"
	"github.com/xraph/relay/acceptance"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	relayid "github.com/xraph/relay/id"
	"github.com/xraph/relay/signature"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

// FleetDestination is operator configuration, never a URL supplied by an instance caller.
// Changing its revision revokes queued deliveries to the previous destination.
type FleetDestination struct {
	ID       string `json:"id"`
	OrgID    string `json:"org_id"`
	FleetID  string `json:"fleet_id"`
	URL      string `json:"url"`
	Revision int64  `json:"revision"`
}

type fleetDestinationRegistry struct {
	mu           sync.RWMutex
	destinations map[string]FleetDestination
}

func fleetTenant(org, fleetID string) string {
	sum := sha256.Sum256([]byte(org + "\x00" + fleetID))
	return "fleet:" + hex.EncodeToString(sum[:])
}
func fleetDestinationKey(org, fleetID, id string) string { return org + "\x00" + fleetID + "\x00" + id }

// SetFleetDestinations replaces the operator's allowlist. An empty list revokes all destinations.
// Configure this before startup, or replace it atomically when operator policy changes.
func (s *Service) SetFleetDestinations(destinations []FleetDestination) error {
	if len(destinations) > 1000 {
		return ErrInvalid
	}
	approved := make(map[string]FleetDestination, len(destinations))
	for _, d := range destinations {
		if d.ID == "" || d.OrgID == "" || d.FleetID == "" || d.Revision < 1 || len(d.ID) > 128 || strings.ContainsAny(d.ID+d.OrgID+d.FleetID, "\x00\r\n") {
			return ErrInvalid
		}
		if err := validateWebhookURL(d.URL); err != nil {
			return ErrInvalid
		}
		parsed, err := url.Parse(d.URL)
		if err != nil || parsed.RawQuery != "" {
			return ErrInvalid
		}
		key := fleetDestinationKey(d.OrgID, d.FleetID, d.ID)
		if _, exists := approved[key]; exists {
			return ErrInvalid
		}
		approved[key] = d
	}
	s.relayStore.fleetDestinations.mu.Lock()
	s.relayStore.fleetDestinations.destinations = approved
	s.relayStore.fleetDestinations.mu.Unlock()
	return nil
}
func (s *encryptedRelayStore) checkFleetEndpoint(ep *endpoint.Endpoint) error {
	if !strings.HasPrefix(ep.TenantID, "fleet:") {
		return nil
	}
	if !ep.Enabled || ep.ScopeOrgID == "" || ep.ScopeAppID == "" || len(ep.Headers) != 0 {
		return relay.ErrEndpointNotFound
	}
	org, fleetID, id := ep.ScopeOrgID, ep.Metadata["fleet_id"], ep.Metadata["destination_id"]
	if ep.TenantID != fleetTenant(org, fleetID) {
		return relay.ErrEndpointNotFound
	}
	s.fleetDestinations.mu.RLock()
	d, ok := s.fleetDestinations.destinations[fleetDestinationKey(org, fleetID, id)]
	s.fleetDestinations.mu.RUnlock()
	if !ok || d.URL != ep.URL || strconv.FormatInt(d.Revision, 10) != ep.Metadata["destination_revision"] {
		return relay.ErrEndpointNotFound
	}
	return nil
}

// RegisterFleetEndpoint uses only a configured destination ID and requires a human operator.
func (s *Service) RegisterFleetEndpoint(ctx context.Context, p security.Principal, fleetID, destinationID string) (WebhookEndpointSecret, error) {
	if p.IsServiceAccount() || p.KeyID != "" || p.ReadOnly {
		return WebhookEndpointSecret{}, security.ErrForbidden
	}
	if err := s.require(ctx, p, security.MemberManage, ""); err != nil {
		return WebhookEndpointSecret{}, err
	}
	var project string
	if err := s.pg.QueryRow(ctx, `SELECT project_id FROM loomplane_fleets WHERE org_id=$1 AND id=$2`, p.OrgID, fleetID).Scan(&project); err != nil {
		return WebhookEndpointSecret{}, ErrNotFound
	}
	if err := s.require(ctx, p, security.ProjectUpdate, project); err != nil {
		return WebhookEndpointSecret{}, err
	}
	s.relayStore.fleetDestinations.mu.RLock()
	d, ok := s.relayStore.fleetDestinations.destinations[fleetDestinationKey(p.OrgID, fleetID, destinationID)]
	s.relayStore.fleetDestinations.mu.RUnlock()
	if !ok {
		return WebhookEndpointSecret{}, ErrNotFound
	}
	eps, err := s.relayStore.ListEndpoints(ctx, fleetTenant(p.OrgID, fleetID), endpoint.ListOpts{Limit: 101})
	if err != nil {
		return WebhookEndpointSecret{}, err
	}
	if len(eps) >= 100 {
		return WebhookEndpointSecret{}, ErrInvalid
	}
	for _, ep := range eps {
		if ep.Enabled && ep.Metadata["destination_id"] == d.ID && ep.Metadata["destination_revision"] == strconv.FormatInt(d.Revision, 10) {
			return WebhookEndpointSecret{}, ErrInvalid
		}
	}
	ep := &endpoint.Endpoint{ID: relayid.NewEndpointID(), TenantID: fleetTenant(p.OrgID, fleetID), ScopeAppID: s.cfg.Identity.AppID, ScopeOrgID: p.OrgID, URL: d.URL, Secret: signature.GenerateSecret(), Enabled: true, RateLimit: 5, EventTypes: []string{"fleet.lifecycle", "fleet.scaling", "fleet.release"}, Metadata: map[string]string{"fleet_id": fleetID, "destination_id": d.ID, "destination_revision": strconv.FormatInt(d.Revision, 10)}}
	ep.CreatedAt = time.Now().UTC()
	ep.UpdatedAt = ep.CreatedAt
	if checkErr := s.relayStore.CreateEndpoint(ctx, ep); checkErr != nil {
		return WebhookEndpointSecret{}, checkErr
	}
	s.recordAudit(ctx, p, "fleet.endpoint.register", ep.ID.String())
	return WebhookEndpointSecret{Endpoint: publicEndpoint(ep), Secret: ep.Secret}, nil
}

// DeliverFleetEvent accepts a stable event into Relay's durable scoped fanout.
// The receipt proves acceptance; delivery status remains available in Relay.
func (s *Service) DeliverFleetEvent(ctx context.Context, org string, event fleet.Event) error {
	if org == "" || event.OrgID != org || event.Version != 1 || event.FleetID == "" || event.InstanceID == "" || event.ID == "" || event.Generation < 1 || event.Sequence < 1 {
		return ErrInvalid
	}
	if event.OccurredAt.IsZero() || event.OccurredAt.Before(time.Now().Add(-time.Duration(s.fleetRetentionDays())*24*time.Hour)) {
		return ErrDeliveryFailed
	}
	switch event.Kind {
	case "lifecycle", "scaling", "release":
	default:
		return ErrInvalid
	}
	endpoints, err := s.relayStore.ListEndpoints(ctx, fleetTenant(org, event.FleetID), endpoint.ListOpts{Limit: 100})
	if err != nil {
		return ErrDeliveryFailed
	}
	active := false
	for _, ep := range endpoints {
		if s.relayStore.checkFleetEndpoint(ep) == nil && ep.ScopeAppID == s.cfg.Identity.AppID {
			active = true
			break
		}
	}
	if !active {
		return ErrDeliveryFailed
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return ErrInvalid
	}
	sum := sha256.Sum256(payload)
	_, err = s.relay.SendReliable(ctx, acceptance.Request{Producer: "loomplane-fleet", InstallationID: s.cfg.Identity.AppID, SourceKey: event.ID, SourceFingerprint: hex.EncodeToString(sum[:]), AppID: s.cfg.Identity.AppID, OrgID: org, TenantID: fleetTenant(org, event.FleetID), Type: "fleet." + event.Kind, Data: payload})
	if err != nil {
		return ErrDeliveryFailed
	}
	return nil
}
func (s *Service) registerFleetEventTypes(ctx context.Context) error {
	for _, kind := range []string{"lifecycle", "scaling", "release"} {
		if _, err := s.relay.RegisterEventType(ctx, catalog.WebhookDefinition{Name: "fleet." + kind, Description: "An instance fleet decision was recorded", Group: "fleet", Version: "1"}); err != nil {
			return err
		}
	}
	return nil
}

// Preserve stable status fields while discarding receiver-controlled diagnostics.
func (s *encryptedRelayStore) UpdateDelivery(ctx context.Context, d *delivery.Delivery) error {
	if !strings.HasPrefix(d.TenantID, "fleet:") {
		return s.Store.UpdateDelivery(ctx, d)
	}
	copyDelivery := *d
	copyDelivery.LastResponse = ""
	if copyDelivery.LastError != "" {
		copyDelivery.LastError = "delivery failed"
	}
	return s.finishFleetDelivery(ctx, &copyDelivery)
}
func (s *encryptedRelayStore) RecordAttempt(ctx context.Context, a *delivery.Attempt) error {
	d, err := s.GetDelivery(ctx, a.DeliveryID)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(d.TenantID, "fleet:") {
		return s.Store.RecordAttempt(ctx, a)
	}
	cp := *a
	cp.Response = ""
	if cp.Error != "" {
		cp.Error = "delivery failed"
	}
	return s.recordFleetAttempt(ctx, d, &cp)
}
func (s *encryptedRelayStore) Push(ctx context.Context, e *dlq.Entry) error {
	if !strings.HasPrefix(e.TenantID, "fleet:") {
		return s.Store.Push(ctx, e)
	}
	cp := *e
	cp.URL = ""
	if cp.Error != "" {
		cp.Error = "delivery failed"
	}
	if s.fleetPG != nil {
		return s.pushFleetDeadLetter(ctx, &cp)
	}
	event, err := s.GetEvent(ctx, e.EventID)
	if err != nil {
		return ErrNotFound
	}
	cp.Payload = event.Data
	cp.URL = ""
	if cp.Error != "" {
		cp.Error = "delivery failed"
	}
	return s.Store.Push(ctx, &cp)
}

// FleetEventDrainer reconciles journal acceptance without sending external traffic itself.
type FleetEventDrainer interface {
	DrainEventDeliveries(context.Context) error
	PruneEventHistory(context.Context, time.Time) error
}

// SetFleetEventDrainer connects the existing notification worker before Run.
func (s *Service) SetFleetEventDrainer(d FleetEventDrainer) { s.fleetEvents = d }

func (s *Service) drainFleetEvents(ctx context.Context) error {
	if s.fleetEvents == nil {
		return nil
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.fleetEvents.DrainEventDeliveries(bounded)
}

func (s *Service) fleetOperator(ctx context.Context, p security.Principal, fleetID string) error {
	if p.IsServiceAccount() || p.KeyID != "" || p.ReadOnly {
		return security.ErrForbidden
	}
	if err := s.require(ctx, p, security.MemberManage, ""); err != nil {
		return err
	}
	var project string
	if err := s.pg.QueryRow(ctx, `SELECT project_id FROM loomplane_fleets WHERE org_id=$1 AND id=$2`, p.OrgID, fleetID).Scan(&project); err != nil {
		return ErrNotFound
	}
	return s.require(ctx, p, security.ProjectUpdate, project)
}

// ListFleetDeliveries exposes bounded delivery and dead-letter status without receiver text.
func (s *Service) ListFleetDeliveries(ctx context.Context, p security.Principal, fleetID, endpointID string) (DeliveryList, error) {
	out := DeliveryList{Items: []Delivery{}}
	if err := s.fleetOperator(ctx, p, fleetID); err != nil {
		return out, err
	}
	parsed, err := relayid.ParseEndpointID(endpointID)
	if err != nil {
		return out, ErrNotFound
	}
	// Read metadata directly so an operator can inspect a revoked destination's failures.
	ep, err := s.relayStore.Store.GetEndpoint(ctx, parsed)
	if err != nil || ep.TenantID != fleetTenant(p.OrgID, fleetID) || ep.ScopeOrgID != p.OrgID || ep.ScopeAppID != s.cfg.Identity.AppID {
		return out, ErrNotFound
	}
	records, err := s.relayStore.ListByEndpoint(ctx, parsed, delivery.ListOpts{Limit: 50})
	if err != nil {
		return out, err
	}
	for _, d := range records {
		if d.TenantID != ep.TenantID {
			continue
		}
		item := Delivery{ID: d.ID.String(), EventType: d.EventType, Status: string(d.State), Attempts: d.AttemptCount, CreatedAt: d.CreatedAt, StatusCode: d.LastStatusCode}
		if d.State == delivery.StateDelivered {
			item.DeliveredAt = d.CompletedAt
		}
		if d.State == delivery.StatePending {
			next := d.NextAttemptAt
			item.NextRetryAt = &next
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// RotateFleetEndpoint keeps queued event IDs stable while using the current signing key.
func (s *Service) RotateFleetEndpoint(ctx context.Context, p security.Principal, fleetID, endpointID string) (WebhookEndpointSecret, error) {
	if err := s.fleetOperator(ctx, p, fleetID); err != nil {
		return WebhookEndpointSecret{}, err
	}
	parsed, err := relayid.ParseEndpointID(endpointID)
	if err != nil {
		return WebhookEndpointSecret{}, ErrNotFound
	}
	ep, err := s.relayStore.GetEndpoint(ctx, parsed)
	if err != nil || ep.TenantID != fleetTenant(p.OrgID, fleetID) {
		return WebhookEndpointSecret{}, ErrNotFound
	}
	secret, err := s.relay.Endpoints().RotateSecret(ctx, ep.ID)
	if err != nil {
		return WebhookEndpointSecret{}, err
	}
	s.recordAudit(ctx, p, "fleet.endpoint.rotate", ep.ID.String())
	return WebhookEndpointSecret{Endpoint: publicEndpoint(ep), Secret: secret}, nil
}

// RevokeFleetEndpoint disables a receiver before the next delivery checks its policy.
func (s *Service) RevokeFleetEndpoint(ctx context.Context, p security.Principal, fleetID, endpointID string) error {
	if err := s.fleetOperator(ctx, p, fleetID); err != nil {
		return err
	}
	parsed, err := relayid.ParseEndpointID(endpointID)
	if err != nil {
		return ErrNotFound
	}
	raw, err := s.relayStore.Store.GetEndpoint(ctx, parsed)
	if err != nil || raw.TenantID != fleetTenant(p.OrgID, fleetID) {
		return ErrNotFound
	}
	ep, err := s.relayStore.decrypt(raw)
	if err != nil {
		return err
	}
	ep.Enabled = false
	ep.UpdatedAt = time.Now().UTC()
	if checkErr := s.relayStore.UpdateEndpoint(ctx, ep); checkErr != nil {
		return checkErr
	}
	s.recordAudit(ctx, p, "fleet.endpoint.revoke", ep.ID.String())
	return nil
}

// loadFleetDestinations reads operator configuration once during startup.
func (s *Service) loadFleetDestinations(path string) error {
	if s.cfg.Notifications.FleetEventRetentionDays < 0 || s.cfg.Notifications.FleetEventRetentionDays > 3650 {
		return errors.New("invalid fleet event retention policy")
	}
	if path == "" {
		return s.SetFleetDestinations(nil)
	}
	file, err := os.Open(path)
	if err != nil {
		return errors.New("fleet destination configuration could not be read")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 256*1024+1))
	if err != nil || len(data) > 256*1024 {
		return errors.New("fleet destination configuration exceeds its size limit or could not be read")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var destinations []FleetDestination
	if err = decoder.Decode(&destinations); err != nil || destinations == nil {
		return errors.New("invalid fleet destination configuration")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("invalid fleet destination configuration")
	}
	if err = s.SetFleetDestinations(destinations); err != nil {
		return errors.New("invalid fleet destination policy")
	}
	return nil
}

// FleetDeliveries pages through receiver outcomes without exposing raw responses.
func (s *Service) FleetDeliveries(ctx context.Context, p security.Principal, fleetID string, in fleet.CallbackQuery) (*fleet.CallbackPage, error) {
	if err := s.fleetOperator(ctx, p, fleetID); err != nil {
		return nil, err
	}
	return s.fleetDeliveryPage(ctx, p.OrgID, fleetID, in)
}
func (s *Service) fleetDeliveryPage(ctx context.Context, org, fleetID string, in fleet.CallbackQuery) (*fleet.CallbackPage, error) {
	if in.Limit < 0 || in.Limit > 200 || len(in.After) > 512 {
		return nil, ErrInvalid
	}
	query := delivery.Query{TenantID: fleetTenant(org, fleetID), Limit: in.Limit, Cursor: in.After}
	if in.State != "" {
		state := delivery.State(in.State)
		switch state {
		case delivery.StatePending, delivery.StateDelivering, delivery.StateDelivered, delivery.StateFailed:
		default:
			return nil, ErrInvalid
		}
		query.State = &state
	}
	if in.EndpointID != "" {
		id, err := relayid.ParseEndpointID(in.EndpointID)
		if err != nil {
			return nil, ErrNotFound
		}
		ep, err := s.relayStore.Store.GetEndpoint(ctx, id)
		if err != nil || ep.TenantID != query.TenantID || ep.ScopeAppID != s.cfg.Identity.AppID {
			return nil, ErrNotFound
		}
		query.EndpointID = &id
	}
	page, err := s.relayStore.ListDeliveries(ctx, query)
	if err != nil {
		return nil, ErrInvalid
	}
	out := &fleet.CallbackPage{Items: make([]fleet.CallbackDelivery, 0, len(page.Deliveries)), NextCursor: page.NextCursor, Complete: page.Complete}
	for _, d := range page.Deliveries {
		if d.TenantID != query.TenantID {
			return nil, ErrNotFound
		}
		event, err := s.relayStore.GetEvent(ctx, d.EventID)
		if err != nil {
			return nil, ErrNotFound
		}
		raw, err := json.Marshal(event.Data)
		if err != nil {
			return nil, ErrInvalid
		}
		var source fleet.Event
		if err = json.Unmarshal(raw, &source); err != nil || source.OrgID != org || source.FleetID != fleetID {
			return nil, ErrNotFound
		}
		item := fleet.CallbackDelivery{ID: d.ID.String(), EventID: source.ID, InstanceID: source.InstanceID, Generation: source.Generation, EndpointID: d.EndpointID.String(), State: string(d.State), Attempts: d.AttemptCount, CreatedAt: d.CreatedAt, CompletedAt: d.CompletedAt}
		if d.State == delivery.StatePending {
			next := d.NextAttemptAt
			item.NextAttemptAt = &next
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

func (s *Service) fleetRetentionDays() int {
	days := s.cfg.Notifications.FleetEventRetentionDays
	if days == 0 {
		return 30
	}
	return days
}

// pruneFleetHistory applies one bounded pass to all detailed copies. Minimal
// identity receipts remain available for deduplication and audit retention.
func (s *Service) pruneFleetHistory(ctx context.Context) error {
	if s.fleetEvents == nil || time.Since(s.fleetPrunedAt) < time.Minute {
		return nil
	}
	s.fleetPrunedAt = time.Now().UTC()
	before := s.fleetPrunedAt.Add(-time.Duration(s.fleetRetentionDays()) * 24 * time.Hour)
	if err := s.pruneFleetRelayHistory(ctx, before, 1000); err != nil {
		return err
	}
	return s.fleetEvents.PruneEventHistory(ctx, before)
}
func (s *Service) pruneFleetRelayHistory(ctx context.Context, before time.Time, limit int) (err error) {
	if before.IsZero() || before.After(time.Now().Add(-24*time.Hour)) || limit < 1 || limit > 1000 {
		return ErrInvalid
	}
	tx, err := s.pg.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				err = errors.Join(err, rollbackErr)
			}
		}
	}()
	rows, err := tx.Query(ctx, `UPDATE relay_events SET data=jsonb_build_object('version',1,'id',data->'id','org_id',data->'org_id','fleet_id',data->'fleet_id','instance_id',data->'instance_id','generation',data->'generation','sequence',data->'sequence','occurred_at',data->'occurred_at','retained',true) WHERE id IN (SELECT id FROM relay_events WHERE tenant_id LIKE 'fleet:%' AND coalesce((data->>'occurred_at')::timestamptz,created_at)<$1 AND data->>'retained' IS DISTINCT FROM 'true' ORDER BY created_at,id LIMIT $2 FOR UPDATE SKIP LOCKED) RETURNING id`, before, limit)
	if err != nil {
		return err
	}
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		if _, err = tx.Exec(ctx, `UPDATE relay_deliveries SET state='failed',last_error='event retention expired',last_response='',completed_at=clock_timestamp(),updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE event_id=ANY($1) AND state IN ('pending','delivering')`, ids); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE relay_dlq q SET payload=e.data,url='',error='delivery failed' FROM relay_events e WHERE q.event_id=e.id AND e.id=ANY($1)`, ids); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM relay_delivery_attempts a USING relay_deliveries d WHERE a.delivery_id=d.id AND d.event_id=ANY($1)`, ids); err != nil {
			return err
		}
	}
	return tx.Commit()
}

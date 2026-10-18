// Package runtimecatalog tracks leased, scoped runtime observations. It does not
// discover repositories or grant permission to call an exported service.
package runtimecatalog

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/telemetry"
	domain "github.com/xraph/loomplane/pkg/federation"
)

const (
	EndpointLease = 30 * time.Second
	MaxBatch      = 1000
	MaxRecords    = 10000
	MaxSnapshot   = 10000
	MaxPage       = 100
)

var (
	ErrUnauthorized  = errors.New("runtime observer or scope is not authorized")
	ErrInvalid       = errors.New("invalid runtime observation")
	ErrStale         = errors.New("runtime observation is stale or replayed")
	ErrCursorExpired = errors.New("runtime snapshot cursor expired; request a full snapshot")
	ErrLimit         = errors.New("runtime catalog limit exceeded")
)

// ExportBounds are server-owned registration constraints, not observer claims.
type ExportBounds struct {
	domain.Scope
	DestinationID string   `json:"destination_id"`
	PlacementID   string   `json:"placement_id"`
	BindingID     string   `json:"binding_id"`
	Port          uint16   `json:"port"`
	CIDRs         []string `json:"cidrs"`
}

// Registration is installed by the authorized enrollment controller. Its identity
// and generation must match the current destination before every store operation.
type Registration struct {
	Agent                 domain.AgentIdentity    `json:"agent"`
	DestinationGeneration int64                   `json:"destination_generation"`
	Active                bool                    `json:"active"`
	Exports               map[string]ExportBounds `json:"exports"`
}

// Record retains a tombstone instead of letting a delayed watch resurrect data.
type Record struct {
	Endpoint  domain.Endpoint           `json:"endpoint"`
	Tombstone *domain.EndpointTombstone `json:"tombstone,omitempty"`
}

type AgentState struct {
	Registration Registration      `json:"registration"`
	Sequence     uint64            `json:"sequence"`
	Records      map[string]Record `json:"records"`
}

// Visibility is an expiring approved policy projection. Missing permissions
// expose no endpoints. The policy controller supplies this revision and scope.
type Visibility struct {
	Revision  string
	ExpiresAt time.Time
	Exports   map[string]domain.Scope
}
type AuthorizeSnapshot func(context.Context, domain.AgentIdentity) (Visibility, error)

// View is an immutable snapshot boundary, persisted for paginated reconnects.
// Each page rechecks agent identity and the effective policy revision.
type View struct {
	ID             string               `json:"id"`
	Agent          domain.AgentIdentity `json:"agent"`
	PolicyRevision string               `json:"policy_revision"`
	Sequence       uint64               `json:"sequence"`
	ExpiresAt      time.Time            `json:"expires_at"`
	IssuedAt       time.Time            `json:"issued_at"`
	Records        []Record             `json:"records"`
	Sources        map[string]string    `json:"sources"`
}

// Store serializes mutation per observer and returns one consistent org revision.
// Reads authorize the exact registered agent before returning any private state.
type Store interface {
	Update(context.Context, domain.AgentIdentity, func(*AgentState) error) error
	Read(context.Context, domain.AgentIdentity) ([]AgentState, uint64, error)
	SaveView(context.Context, *View) error
	GetView(context.Context, domain.AgentIdentity, string) (*View, error)
}

type Catalog struct {
	store     Store
	authorize AuthorizeSnapshot
	now       func() time.Time
	metrics   *telemetry.Recorder
}

func New(store Store, authorize AuthorizeSnapshot, metrics *telemetry.Recorder) *Catalog {
	return &Catalog{store: store, authorize: authorize, now: time.Now, metrics: metrics}
}

func (c *Catalog) Observe(ctx context.Context, agent domain.AgentIdentity, batch domain.ObservationBatch) (err error) {
	defer func() { c.metrics.CatalogOperation("observe", catalogOutcome(err)) }()
	if agent != batch.Agent {
		return ErrUnauthorized
	}
	sequence := batch.Sequence
	if sequence == 0 || sequence > math.MaxInt64 || len(batch.Endpoints)+len(batch.Tombstones) > MaxBatch {
		return ErrInvalid
	}
	generation := int64(sequence)
	now := c.now().UTC()
	return c.store.Update(ctx, agent, func(state *AgentState) error {
		if err := checkAgent(state, agent); err != nil {
			return err
		}
		if batch.DestinationGeneration != state.Registration.DestinationGeneration {
			return ErrStale
		}
		if batch.Sequence <= state.Sequence {
			return ErrStale
		}
		// Complete snapshots remove absent records. After every cached snapshot
		// has expired, the observer sequence fences replay of compacted tombstones.
		for id, record := range state.Records {
			if record.Tombstone != nil && !record.Tombstone.DeletedAt.Add(EndpointLease).After(now) {
				delete(state.Records, id)
			}
		}
		seen := map[string]bool{}
		for _, ep := range batch.Endpoints {
			if seen[ep.ID] {
				return ErrInvalid
			}
			seen[ep.ID] = true
			if err := validateEndpoint(state.Registration, ep); err != nil {
				return err
			}
			if ep.Generation < 1 || ep.Generation > generation {
				return ErrInvalid
			}
			old, exists := state.Records[ep.ID]
			if !exists && (state.Sequence > math.MaxInt64 || ep.Generation <= int64(state.Sequence)) {
				return ErrStale
			}
			if exists && (ep.Generation < recordGeneration(old) || (ep.Generation == recordGeneration(old) && (old.Tombstone != nil || !sameEndpoint(old.Endpoint, ep)))) {
				return ErrStale
			}
			ep.ObservedAt = now
			ep.ExpiresAt = now.Add(EndpointLease)
			ep.CreatedAt = old.Endpoint.CreatedAt
			if ep.CreatedAt.IsZero() {
				ep.CreatedAt = now
			}
			ep.UpdatedAt = now
			state.Records[ep.ID] = Record{Endpoint: ep}
		}
		for _, tomb := range batch.Tombstones {
			if seen[tomb.ID] || !validID(tomb.ID) || tomb.Generation < 1 || uint64(tomb.Generation) > batch.Sequence {
				return ErrInvalid
			}
			seen[tomb.ID] = true
			bounds, ok := state.Registration.Exports[tomb.ExportID]
			if !ok || tomb.Scope != bounds.Scope || tomb.DestinationID != agent.DestinationID || tomb.ObserverID != agent.AgentID {
				return ErrUnauthorized
			}
			old, exists := state.Records[tomb.ID]
			if !exists && uint64(tomb.Generation) <= state.Sequence {
				return ErrStale
			}
			if exists && (tomb.Generation <= recordGeneration(old) || tomb.ExportID != old.Endpoint.ExportID) {
				return ErrStale
			}
			tomb.DeletedAt = now
			tomb.UpdatedAt = now
			tomb.CreatedAt = now
			state.Records[tomb.ID] = Record{Endpoint: domain.Endpoint{Metadata: domain.Metadata{Scope: tomb.Scope, ID: tomb.ID}, DestinationID: tomb.DestinationID, ExportID: tomb.ExportID, ObserverID: tomb.ObserverID}, Tombstone: &tomb}
		}
		if batch.Full {
			for id, old := range state.Records {
				if seen[id] || old.Tombstone != nil {
					continue
				}
				if generation <= recordGeneration(old) {
					return ErrStale
				}
				ep := old.Endpoint
				tomb := &domain.EndpointTombstone{Metadata: domain.Metadata{Scope: ep.Scope, ID: id, Generation: generation, CreatedAt: now, UpdatedAt: now}, DestinationID: agent.DestinationID, ExportID: ep.ExportID, ObserverID: agent.AgentID, DeletedAt: now}
				old.Tombstone = tomb
				state.Records[id] = old
			}
		}
		if len(state.Records) > MaxRecords {
			return ErrLimit
		}
		state.Sequence = batch.Sequence
		return nil
	})
}

func checkAgent(state *AgentState, agent domain.AgentIdentity) error {
	if !state.Registration.Active || state.Registration.Agent != agent || agent.Epoch == 0 || agent.URI == "" || !validScope(agent.Scope) || !validID(agent.AgentID) || !validID(agent.DestinationID) {
		return ErrUnauthorized
	}
	return nil
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func validID(id string) bool { return identifier.MatchString(id) }
func validScope(scope domain.Scope) bool {
	return validID(scope.OrgID) && validID(scope.ProjectID) && validID(scope.EnvironmentID)
}
func recordGeneration(r Record) int64 {
	if r.Tombstone != nil {
		return r.Tombstone.Generation
	}
	return r.Endpoint.Generation
}
func sameEndpoint(a, b domain.Endpoint) bool {
	a.CreatedAt = time.Time{}
	a.UpdatedAt = time.Time{}
	a.ObservedAt = time.Time{}
	a.ExpiresAt = time.Time{}
	b.CreatedAt = time.Time{}
	b.UpdatedAt = time.Time{}
	b.ObservedAt = time.Time{}
	b.ExpiresAt = time.Time{}
	return a == b
}
func validateEndpoint(reg Registration, ep domain.Endpoint) error {
	bounds, ok := reg.Exports[ep.ExportID]
	if !ok || ep.Scope != bounds.Scope || ep.Scope != reg.Agent.Scope || ep.DestinationID != reg.Agent.DestinationID || ep.DestinationID != bounds.DestinationID || ep.ObserverID != reg.Agent.AgentID || ep.BindingID != bounds.BindingID || ep.PlacementID != bounds.PlacementID || ep.Port != bounds.Port {
		return ErrUnauthorized
	}
	if !validID(ep.ID) || ep.Generation < 1 || !slices.Contains([]domain.Readiness{domain.Ready, domain.Unready, domain.Draining, domain.Unknown}, ep.Readiness) || len(ep.Zone) > 128 {
		return ErrInvalid
	}
	addr, err := netip.ParseAddr(ep.Address)
	if err != nil || addr.Zone() != "" || addr.Is4In6() || !addr.IsGlobalUnicast() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr == netip.MustParseAddr("100.100.100.200") || addr == netip.MustParseAddr("168.63.129.16") || addr == netip.MustParseAddr("fd00:ec2::254") {
		return ErrInvalid
	}
	for _, raw := range bounds.CIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err == nil && prefix == prefix.Masked() && prefix.Contains(addr) {
			return nil
		}
	}
	return ErrUnauthorized
}

func (c *Catalog) Snapshot(ctx context.Context, agent domain.AgentIdentity, request domain.SnapshotRequest) (_ *domain.Snapshot, err error) {
	defer func() { c.metrics.CatalogOperation("snapshot", catalogOutcome(err)) }()
	if request.Agent != agent || request.Limit < 0 || request.Limit > MaxPage {
		return nil, ErrInvalid
	}
	if c.authorize == nil {
		return nil, ErrUnauthorized
	}
	visibility, err := c.authorize(ctx, agent)
	if err != nil {
		return nil, err
	}
	now := c.now().UTC()
	if visibility.Revision == "" || !visibility.ExpiresAt.After(now) {
		return nil, ErrUnauthorized
	}
	for _, scope := range visibility.Exports {
		if !validScope(scope) || scope.OrgID != agent.OrgID {
			return nil, ErrUnauthorized
		}
	}
	policyRevision, err := visibilityRevision(visibility)
	if err != nil {
		return nil, err
	}
	limit := request.Limit
	if limit == 0 {
		limit = MaxPage
	}
	var view *View
	offset := 0
	if request.Cursor != "" {
		parts := strings.Split(request.Cursor, ":")
		if len(parts) != 2 || len(parts[0]) != 32 {
			return nil, ErrCursorExpired
		}
		offset, err = strconv.Atoi(parts[1])
		if err != nil || offset < 1 || offset > MaxSnapshot {
			return nil, ErrCursorExpired
		}
		view, err = c.store.GetView(ctx, agent, parts[0])
		if err != nil {
			return nil, err
		}
		if !view.ExpiresAt.After(now) || visibility.ExpiresAt.Before(view.ExpiresAt) || view.PolicyRevision != policyRevision || offset >= len(view.Records) {
			return nil, ErrCursorExpired
		}
		states, _, err := c.store.Read(ctx, agent)
		if err != nil {
			return nil, err
		}
		current := map[string]string{}
		for _, state := range states {
			if !state.Registration.Active {
				continue
			}
			revision, err := registrationRevision(state.Registration)
			if err != nil {
				return nil, err
			}
			current[state.Registration.Agent.AgentID] = revision
		}
		for source, revision := range view.Sources {
			if current[source] != revision {
				return nil, ErrCursorExpired
			}
		}
	} else {
		states, sequence, err := c.store.Read(ctx, agent)
		if err != nil {
			return nil, err
		}
		records := []Record{}
		sources := map[string]string{}
		expiry := now.Add(EndpointLease)
		if visibility.ExpiresAt.Before(expiry) {
			expiry = visibility.ExpiresAt
		}
		for _, state := range states {
			if !state.Registration.Active {
				continue
			}
			sourceRevision, err := registrationRevision(state.Registration)
			if err != nil {
				return nil, err
			}
			for _, record := range state.Records {
				ep := record.Endpoint
				allowed, ok := visibility.Exports[ep.ExportID]
				if !ok || allowed != ep.Scope {
					continue
				}
				if record.Tombstone == nil {
					if ep.Readiness != domain.Ready || !ep.ExpiresAt.After(now) {
						continue
					}
					if ep.ExpiresAt.Before(expiry) {
						expiry = ep.ExpiresAt
					}
				}
				records = append(records, record)
				sources[state.Registration.Agent.AgentID] = sourceRevision
				if len(records) > MaxSnapshot {
					return nil, ErrLimit
				}
			}
		}
		slices.SortFunc(records, func(a, b Record) int { return strings.Compare(recordKey(a), recordKey(b)) })
		token := make([]byte, 16)
		if _, err := rand.Read(token); err != nil {
			return nil, fmt.Errorf("snapshot token: %w", err)
		}
		view = &View{ID: hex.EncodeToString(token), Agent: agent, PolicyRevision: policyRevision, Sequence: sequence, ExpiresAt: expiry, IssuedAt: now, Records: records, Sources: sources}
		if len(records) > limit {
			if err := c.store.SaveView(ctx, view); err != nil {
				return nil, err
			}
		}
	}
	out := &domain.Snapshot{Scope: agent.Scope, DestinationID: agent.DestinationID, Epoch: agent.Epoch, Sequence: view.Sequence, Revision: agent.OrgID + ":" + strconv.FormatUint(view.Sequence, 10) + ":" + view.PolicyRevision, IssuedAt: view.IssuedAt, ExpiresAt: view.ExpiresAt, Full: true, Endpoints: []domain.Endpoint{}, Tombstones: []domain.EndpointTombstone{}}
	end := min(offset+limit, len(view.Records))
	for _, record := range view.Records[offset:end] {
		if record.Tombstone != nil {
			out.Tombstones = append(out.Tombstones, *record.Tombstone)
		} else {
			out.Endpoints = append(out.Endpoints, record.Endpoint)
		}
	}
	if end < len(view.Records) {
		out.NextCursor = view.ID + ":" + strconv.Itoa(end)
	}
	return out, nil
}

func visibilityRevision(visibility Visibility) (string, error) {
	// JSON sorts map keys. Bind the cursor to the exact scope projection even
	// when an upstream controller accidentally reuses its revision label.
	payload, err := json.Marshal(struct {
		Revision string
		Exports  map[string]domain.Scope
	}{visibility.Revision, visibility.Exports})
	if err != nil {
		return "", fmt.Errorf("encode snapshot visibility: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func recordKey(r Record) string {
	e := r.Endpoint
	return e.OrgID + "/" + e.ProjectID + "/" + e.EnvironmentID + "/" + e.DestinationID + "/" + e.ObserverID + "/" + e.ID
}

func registrationRevision(reg Registration) (string, error) {
	payload, err := json.Marshal(reg)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func catalogOutcome(err error) string {
	switch {
	case err == nil:
		return "accepted"
	case errors.Is(err, ErrUnauthorized):
		return "denied"
	case errors.Is(err, ErrStale), errors.Is(err, ErrCursorExpired):
		return "stale"
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrLimit):
		return "invalid"
	default:
		return "failed"
	}
}

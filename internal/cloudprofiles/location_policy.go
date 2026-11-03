package cloudprofiles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/security"
)

// LocationPolicy ranks eligible targets. It never grants expansion authority.
// Revision zero identifies the default policy when no policy has been saved.
type LocationPolicy struct {
	Revision           int64    `json:"revision"`
	Ranking            string   `json:"ranking,omitempty"`
	PreferredLocations []string `json:"preferred_locations,omitempty"`
	PreferredProviders []string `json:"preferred_providers,omitempty"`
}

func (p LocationPolicy) Validate() error {
	if p.Revision < 0 || !slices.Contains([]string{"", "default", "latency"}, p.Ranking) {
		return ErrInvalid
	}
	for _, values := range [][]string{p.PreferredLocations, p.PreferredProviders} {
		if len(values) > 32 {
			return ErrInvalid
		}
		seen := map[string]bool{}
		for _, v := range values {
			if v == "" || len(v) > 128 || seen[v] {
				return ErrInvalid
			}
			seen[v] = true
		}
	}
	return nil
}

type LocationPolicyStore interface {
	GetLocationPolicy(context.Context, string, string) (*LocationPolicy, error)
	SaveLocationPolicy(context.Context, string, string, LocationPolicy) (*LocationPolicy, error)
}

func (s *PgStore) GetLocationPolicy(ctx context.Context, org, cloud string) (*LocationPolicy, error) {
	if _, err := s.GetProfile(ctx, org, "", cloud); err != nil {
		return nil, err
	}
	var raw []byte
	var version int
	var revision int64
	err := s.db.QueryRow(ctx, `SELECT schema_version,revision,body FROM loomplane_cloud_location_policies WHERE org_id=$1 AND cloud_id=$2`, org, cloud).Scan(&version, &revision, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return &LocationPolicy{}, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeLocationPolicy(version, revision, raw)
}

func (s *PgStore) SaveLocationPolicy(ctx context.Context, org, cloud string, in LocationPolicy) (*LocationPolicy, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollbackSync(tx)
	if lockErr := LockOrganization(ctx, tx, org); lockErr != nil {
		return nil, lockErr
	}
	var id string
	if err = tx.QueryRow(ctx, `SELECT id FROM loomplane_cloud_profiles WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, cloud).Scan(&id); err != nil {
		return nil, storeError(err)
	}
	var current int64
	var version int
	var previous []byte
	err = tx.QueryRow(ctx, `SELECT revision,schema_version,body FROM loomplane_cloud_location_policies WHERE org_id=$1 AND cloud_id=$2 FOR UPDATE`, org, cloud).Scan(&current, &version, &previous)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if current > 0 {
		if _, decodeErr := decodeLocationPolicy(version, current, previous); decodeErr != nil {
			return nil, decodeErr
		}
	}
	if current != in.Revision {
		return nil, ErrConflict
	}
	in.Revision++
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_location_policies(org_id,cloud_id,revision,body) VALUES($1,$2,$3,$4) ON CONFLICT(org_id,cloud_id) DO UPDATE SET revision=excluded.revision,body=excluded.body,updated_at=now()`, org, cloud, in.Revision, raw)
	if err != nil {
		return nil, err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, commitErr
	}
	return &in, nil
}

func (s *Service) GetLocationPolicy(ctx context.Context, p security.Principal, cloud string) (*LocationPolicy, error) {
	if err := s.authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID); err != nil {
		return nil, err
	}
	st, ok := s.store.(LocationPolicyStore)
	if !ok {
		return nil, ErrInvalid
	}
	return st.GetLocationPolicy(ctx, p.OrgID, cloud)
}

func (s *Service) SaveLocationPolicy(ctx context.Context, p security.Principal, cloud string, in LocationPolicy) (*LocationPolicy, error) {
	if err := s.authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID); err != nil {
		return nil, err
	}
	if _, err := s.writable(ctx, p.OrgID, "", cloud); err != nil {
		return nil, err
	}
	st, ok := s.store.(LocationPolicyStore)
	if !ok {
		return nil, ErrInvalid
	}
	saved, err := st.SaveLocationPolicy(ctx, p.OrgID, cloud, in)
	if err == nil {
		s.record(ctx, p, "", "cloud_location_policy.save", map[string]any{"cloud_id": cloud, "revision": saved.Revision})
	}
	return saved, err
}

// LocationCandidatePin contains only revision and identity references. Observed
// timestamps stay outside review digests; freshness is checked again on apply.
type LocationCandidatePin struct {
	RuntimeConnectionID  string `json:"runtime_connection_id,omitempty"`
	ConnectionGeneration int64  `json:"connection_generation,omitempty"`
	CapacitySource       string `json:"capacity_source,omitempty"`
	TargetID             string `json:"target_id"`
	TargetRevision       int64  `json:"target_revision"`
	LocationID           string `json:"location_id,omitempty"`
	LocationRevision     int64  `json:"location_revision,omitempty"`
	EvidenceRevision     int64  `json:"evidence_revision,omitempty"`
}

// LocationReview pins every candidate that contributed to a placement decision.
type LocationReview struct {
	Policy           LocationPolicy         `json:"policy"`
	SelectedTargetID string                 `json:"selected_target_id,omitempty"`
	CloudID          string                 `json:"cloud_id"`
	PolicyRevision   int64                  `json:"policy_revision"`
	GrantRevision    int64                  `json:"grant_revision"`
	Candidates       []LocationCandidatePin `json:"candidates"`
}

// ValidateLocationReviewTx runs under the caller's organization lock. A default
// policy pin becomes stale if an operator subsequently creates a policy.
func ValidateLocationReviewTx(ctx context.Context, tx driver.Tx, org, project string, review *LocationReview, now time.Time) error {
	if review == nil {
		return nil
	}
	if now.IsZero() || review.CloudID == "" || review.GrantRevision < 1 || len(review.Candidates) == 0 {
		return ErrInvalid
	}
	var revision int64
	var version int
	var raw []byte
	readErr := tx.QueryRow(ctx, `SELECT revision,schema_version,body FROM loomplane_cloud_location_policies WHERE org_id=$1 AND cloud_id=$2 FOR SHARE`, org, review.CloudID).Scan(&revision, &version, &raw)
	if readErr != nil && !errors.Is(readErr, pgx.ErrNoRows) {
		return readErr
	}
	if revision != review.PolicyRevision {
		return ErrConflict
	}
	if revision > 0 {
		if _, decodeErr := decodeLocationPolicy(version, revision, raw); decodeErr != nil {
			return decodeErr
		}
	}
	var grant int64
	if err := tx.QueryRow(ctx, `SELECT revision FROM loomplane_cloud_project_grants WHERE org_id=$1 AND project_id=$2 AND cloud_id=$3 AND NOT revoked FOR SHARE`, org, project, review.CloudID).Scan(&grant); err != nil {
		return storeError(err)
	}
	if grant != review.GrantRevision {
		return ErrConflict
	}
	for _, pin := range review.Candidates {
		var revision int64
		var location string
		err := tx.QueryRow(ctx, `SELECT t.revision,coalesce(t.location_id,'') FROM loomplane_cloud_targets t JOIN loomplane_cloud_project_grants g ON g.org_id=t.org_id AND g.cloud_id=t.cloud_id WHERE t.org_id=$1 AND t.cloud_id=$2 AND t.id=$3 AND g.project_id=$4 AND NOT g.revoked AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids)) FOR SHARE OF t,g`, org, review.CloudID, pin.TargetID, project).Scan(&revision, &location)
		if err != nil {
			return storeError(err)
		}
		if revision != pin.TargetRevision || location != pin.LocationID {
			return ErrConflict
		}
		if pin.TargetID == review.SelectedTargetID {
			if runtimeErr := validateLocationRuntimeTx(ctx, tx, org, pin, now); runtimeErr != nil {
				return runtimeErr
			}
		}
		if location == "" {
			continue
		}
		l, err := scanLocation(tx.QueryRow(ctx, `SELECT body FROM loomplane_cloud_locations WHERE org_id=$1 AND cloud_id=$2 AND id=$3 FOR SHARE`, org, review.CloudID, location))
		if err != nil {
			return err
		}
		if l.Revision != pin.LocationRevision || l.EvidenceRevision != pin.EvidenceRevision {
			return ErrConflict
		}
		if l.EvidenceRevision > 0 && (l.CheckedAt.IsZero() || l.CheckedAt.After(now.Add(time.Minute))) {
			return fmt.Errorf("%w: location evidence clock is invalid", ErrConflict)
		}
	}
	return nil
}

func (s *PgStore) ValidateLocationReview(ctx context.Context, org, project string, review *LocationReview, now time.Time) error {
	if review == nil {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackSync(tx)
	if lockErr := LockOrganization(ctx, tx, org); lockErr != nil {
		return lockErr
	}
	if checkErr := ValidateLocationReviewTx(ctx, tx, org, project, review, now); checkErr != nil {
		return checkErr
	}
	return tx.Commit()
}

// PlacementLocation reads private facts only after a current project target grant.
func (s *PgStore) PlacementLocation(ctx context.Context, org, project, cloud, id string) (*CloudLocation, error) {
	if project == "" {
		return nil, ErrInvalid
	}
	return scanLocation(s.db.QueryRow(ctx, `SELECT body FROM loomplane_cloud_locations l WHERE org_id=$1 AND `+locationGrant+` AND cloud_id=$3 AND id=$4`, org, project, cloud, id))
}

func validateLocationRuntimeTx(ctx context.Context, tx driver.Tx, org string, pin LocationCandidatePin, now time.Time) error {
	if pin.RuntimeConnectionID == "" || pin.ConnectionGeneration < 1 {
		return ErrConflict
	}
	var generation int64
	var checked *time.Time
	var status string
	if err := tx.QueryRow(ctx, `SELECT generation,checked_at,status FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2 FOR SHARE`, org, pin.RuntimeConnectionID).Scan(&generation, &checked, &status); err != nil {
		return storeError(err)
	}
	if generation != pin.ConnectionGeneration || status != "ready" {
		return ErrConflict
	}
	maxAge := 5 * time.Minute
	switch pin.CapacitySource {
	case "server":
		if err := tx.QueryRow(ctx, `SELECT (body->'health'->>'checked_at')::timestamptz FROM loomplane_cloud_servers WHERE org_id=$1 AND runtime_connection_id=$2 AND status NOT IN ('deleted','disconnected') FOR SHARE`, org, pin.RuntimeConnectionID).Scan(&checked); err != nil {
			return storeError(err)
		}
		maxAge = 15 * time.Minute
	case "managed-cluster":
		var complete bool
		if err := tx.QueryRow(ctx, `SELECT (i.body->>'checked_at')::timestamptz,coalesce((i.body->>'complete')::boolean,false) AND coalesce((i.body->>'workloads_complete')::boolean,false) FROM loomplane_runtime_attachments a JOIN loomplane_cluster_capacity_inventory i ON i.org_id=a.org_id AND i.resource_id=a.cluster_id AND i.saved_revision=i.issued_revision WHERE a.org_id=$1 AND a.runtime_connection_id=$2 AND a.deleted_at IS NULL FOR SHARE OF a,i`, org, pin.RuntimeConnectionID).Scan(&checked, &complete); err != nil {
			return storeError(err)
		}
		if !complete {
			return ErrConflict
		}
		maxAge = 2 * time.Minute
	case "expansion":
		return nil // The capacity template, current quote and quota guards run before any purchase.
	case "managed":
	default:
		return ErrConflict
	}
	if checked == nil || checked.IsZero() || checked.After(now.Add(time.Minute)) || !checked.Add(maxAge).After(now) {
		return fmt.Errorf("%w: location runtime evidence expired", ErrConflict)
	}
	return nil
}

func decodeLocationPolicy(version int, revision int64, raw []byte) (*LocationPolicy, error) {
	if version != 1 || revision < 1 {
		return nil, fmt.Errorf("%w: location policy schema is unsupported", ErrInvalid)
	}
	var policy LocationPolicy
	if err := json.Unmarshal(raw, &policy); err != nil {
		return nil, ErrInvalid
	}
	if policy.Revision != revision {
		return nil, ErrInvalid
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &policy, nil
}

package fleets

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

// ReleaseStore persists complete immutable snapshots, scoped to their owning fleet.
type ReleaseStore interface {
	PublishRelease(context.Context, FleetScope, *fleet.TemplateRelease) (*fleet.TemplateRelease, error)
	GetRelease(context.Context, FleetScope, string) (*fleet.TemplateRelease, error)
}

// PublishRelease accepts operator-authored artifacts. Publication creates no resources.
func (s *Service) PublishRelease(ctx context.Context, p security.Principal, fleetID string, raw []byte) (*fleet.TemplateRelease, error) {
	if p.IsServiceAccount() {
		return nil, security.ErrForbidden
	}
	f, err := s.store.GetFleet(ctx, p.OrgID, fleetID)
	if err != nil {
		return nil, err
	}
	if authErr := s.authz.Require(ctx, p, security.ProjectUpdate, f.ProjectID); authErr != nil {
		return nil, authErr
	}
	if f.Status == "retired" {
		return nil, ErrConflict
	}
	t, err := fleet.ParseTemplate(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	digest, err := fleet.CanonicalDigest(t)
	if err != nil {
		return nil, err
	}
	store, ok := s.store.(ReleaseStore)
	if !ok {
		return nil, fmt.Errorf("%w: release storage unavailable", ErrInvalid)
	}
	release, err := store.PublishRelease(ctx, FleetScope{OrgID: p.OrgID, ProjectID: f.ProjectID, FleetID: f.ID}, &fleet.TemplateRelease{ID: uuid.NewString(), FleetID: f.ID, Digest: digest, Template: *t, CreatedAt: time.Now().UTC()})
	if err != nil {
		return nil, err
	}
	s.record(ctx, p, f.ProjectID, "fleet.release.publish", release.ID)
	return release, nil
}

// PublishRelease deduplicates by digest without ever updating a published row.
func (s *PgStore) PublishRelease(ctx context.Context, scope FleetScope, r *fleet.TemplateRelease) (*fleet.TemplateRelease, error) {
	if r == nil || r.FleetID != scope.FleetID {
		return nil, ErrInvalid
	}
	raw, err := fleet.CanonicalTemplate(&r.Template)
	if err != nil {
		return nil, err
	}
	digest, err := fleet.CanonicalDigest(&r.Template)
	if err != nil {
		return nil, err
	}
	if digest != r.Digest {
		return nil, ErrInvalid
	}
	_, err = s.db.Exec(ctx, `INSERT INTO loomplane_fleet_releases(id,org_id,project_id,fleet_id,digest,snapshot,created_at) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(org_id,fleet_id,digest) DO NOTHING`, r.ID, scope.OrgID, scope.ProjectID, scope.FleetID, r.Digest, string(raw), r.CreatedAt)
	if err != nil {
		return nil, storeError(err)
	}
	return scanRelease(s.db.QueryRow(ctx, `SELECT id,fleet_id,digest,snapshot,created_at FROM loomplane_fleet_releases WHERE org_id=$1 AND project_id=$2 AND fleet_id=$3 AND digest=$4`, scope.OrgID, scope.ProjectID, scope.FleetID, r.Digest))
}

// GetRelease returns a newly decoded copy, never an alias of the stored snapshot.
func (s *PgStore) GetRelease(ctx context.Context, scope FleetScope, id string) (*fleet.TemplateRelease, error) {
	return scanRelease(s.db.QueryRow(ctx, `SELECT id,fleet_id,digest,snapshot,created_at FROM loomplane_fleet_releases WHERE org_id=$1 AND project_id=$2 AND fleet_id=$3 AND id=$4`, scope.OrgID, scope.ProjectID, scope.FleetID, id))
}
func scanRelease(row scanner) (*fleet.TemplateRelease, error) {
	var r fleet.TemplateRelease
	var raw string
	if err := row.Scan(&r.ID, &r.FleetID, &r.Digest, &raw, &r.CreatedAt); err != nil {
		return nil, storeError(err)
	}
	t, err := fleet.ParseTemplate([]byte(raw))
	if err != nil {
		return nil, err
	}
	digest, err := fleet.CanonicalDigest(t)
	if err != nil {
		return nil, err
	}
	if digest != r.Digest {
		return nil, fmt.Errorf("%w: stored release digest mismatch", ErrConflict)
	}
	r.Template = *t
	r.CreatedAt = r.CreatedAt.UTC()
	return &r, nil
}

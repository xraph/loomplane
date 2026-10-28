package cloudinfra

import (
	"context"
	"encoding/json"

	"github.com/xraph/loomplane/internal/security"
)

// ListExpansions lists a Cloud's recent expansions, newest first, so people
// can see what is waiting for approval and what was added. Cancelled ones
// are left out.
func (s *Service) ListExpansions(ctx context.Context, p security.Principal, cloudID string) ([]*Expansion, error) {
	if _, err := s.authorize(ctx, p, cloudID); err != nil {
		return nil, err
	}
	st, ok := s.Store.(*PgStore)
	if !ok {
		return nil, ErrInvalid
	}
	return st.ListExpansions(ctx, p.OrgID, cloudID)
}

// AutomaticExpansion reports whether pools may grow on their own. While it's
// off, policies save but no expansion is created or approved.
func (s *Service) AutomaticExpansion() bool { return s.EnableAutomaticCapacity }

// ListExpansions reads the newest 100 expansions of a Cloud that weren't cancelled.
func (s *PgStore) ListExpansions(ctx context.Context, org, cloud string) ([]*Expansion, error) {
	rows, err := s.db.Query(ctx, `SELECT body FROM loomplane_cloud_expansions WHERE org_id=$1 AND cloud_id=$2 AND status<>'cancelled' ORDER BY created_at DESC,id DESC LIMIT 100`, org, cloud)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Expansion{}
	for rows.Next() {
		var raw []byte
		if scanErr := rows.Scan(&raw); scanErr != nil {
			return nil, scanErr
		}
		var e Expansion
		if decodeErr := json.Unmarshal(raw, &e); decodeErr != nil {
			return nil, decodeErr
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

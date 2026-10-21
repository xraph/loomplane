package recovery

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/xraph/loomplane/internal/security"
)

// RetentionPolicyState distinguishes an unset policy from failed inspection.
type RetentionPolicyState struct {
	Configured bool             `json:"configured"`
	Policy     *RetentionPolicy `json:"policy,omitempty"`
}

// RetentionPolicyRevision records an owner's committed settings and authority.
type RetentionPolicyRevision struct {
	Revision     int64          `json:"revision"`
	Policy       RetentionInput `json:"policy"`
	Enabled      bool           `json:"enabled"`
	AuthorizedBy string         `json:"authorized_by"`
	CreatedAt    time.Time      `json:"created_at"`
}

type RetentionPolicyHistory struct {
	Events     []RetentionPolicyRevision `json:"events"`
	NextBefore string                    `json:"next_before,omitempty"`
}

type RetentionPolicyHistoryInput RetirementHistoryInput

func (in RetentionPolicyHistoryInput) Validate() error {
	_, _, err := in.bounds()
	return err
}

func (in RetentionPolicyHistoryInput) bounds() (limit int, before int64, err error) {
	return RetirementHistoryInput(in).bounds()
}

func (s *PgStore) RetentionPolicyHistory(ctx context.Context, org, project, id string, in RetentionPolicyHistoryInput) (*RetentionPolicyHistory, error) {
	policy, err := s.GetRetentionPolicy(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if policy.ProjectID != project {
		return nil, ErrNotFound
	}
	limit, before, err := in.bounds()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT revision,policy,enabled,authorized_by,created_at FROM loomplane_retention_policy_revisions
 WHERE org_id=$1 AND project_id=$2 AND policy_id=$3 AND ($4::bigint=0 OR revision<$4) ORDER BY revision DESC LIMIT $5`, org, project, id, before, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &RetentionPolicyHistory{Events: []RetentionPolicyRevision{}}
	for rows.Next() {
		var event RetentionPolicyRevision
		var raw []byte
		if err := rows.Scan(&event.Revision, &raw, &event.Enabled, &event.AuthorizedBy, &event.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &event.Policy); err != nil {
			return nil, err
		}
		page.Events = append(page.Events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(page.Events) > limit {
		page.Events = page.Events[:limit]
		page.NextBefore = strconv.FormatInt(page.Events[limit-1].Revision, 10)
	}
	return page, nil
}

func (s *Service) RetentionPolicyHistoryForBackup(ctx context.Context, p security.Principal, anchorID string, in RetentionPolicyHistoryInput) (*RetentionPolicyHistory, error) {
	policy, err := s.RetentionPolicyForBackup(ctx, p, anchorID)
	if err != nil {
		return nil, err
	}
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if policy == nil {
		return &RetentionPolicyHistory{Events: []RetentionPolicyRevision{}}, nil
	}
	return s.RetentionPolicies.RetentionPolicyHistory(ctx, p.OrgID, policy.ProjectID, policy.ID, in)
}

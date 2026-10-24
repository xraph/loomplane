package recovery

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/xraph/loomplane/internal/security"
)

func (s *PgStore) NativeRetentionPolicyHistory(ctx context.Context, org, source string, in RetentionPolicyHistoryInput) (*RetentionPolicyHistory, error) {
	policy, err := s.NativeRetentionPolicyForSource(ctx, org, source)
	if err != nil {
		return nil, err
	}
	limit, before, err := in.bounds()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT revision,policy,enabled,authorized_by,created_at FROM loomplane_native_retention_revisions WHERE org_id=$1 AND policy_id=$2 AND ($3::bigint=0 OR revision<$3) ORDER BY revision DESC LIMIT $4`, org, policy.ID, before, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &RetentionPolicyHistory{Events: []RetentionPolicyRevision{}}
	for rows.Next() {
		var event RetentionPolicyRevision
		var raw []byte
		if scanErr := rows.Scan(&event.Revision, &raw, &event.Enabled, &event.AuthorizedBy, &event.CreatedAt); scanErr != nil {
			return nil, scanErr
		}
		if decodeErr := json.Unmarshal(raw, &event.Policy); decodeErr != nil {
			return nil, decodeErr
		}
		out.Events = append(out.Events, event)
	}
	if readErr := rows.Err(); readErr != nil {
		return nil, readErr
	}
	if len(out.Events) > limit {
		out.Events = out.Events[:limit]
		out.NextBefore = strconv.FormatInt(out.Events[limit-1].Revision, 10)
	}
	return out, nil
}
func (s *NativeService) NativeRetentionPolicyHistory(ctx context.Context, p security.Principal, source string, in RetentionPolicyHistoryInput) (*RetentionPolicyHistory, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	policy, err := s.NativeRetentionPolicy(ctx, p, source)
	if err != nil {
		return nil, err
	}
	if policy == nil {
		return &RetentionPolicyHistory{Events: []RetentionPolicyRevision{}}, nil
	}
	store, ok := s.Store.(interface {
		NativeRetentionPolicyHistory(context.Context, string, string, RetentionPolicyHistoryInput) (*RetentionPolicyHistory, error)
	})
	if !ok {
		return nil, ErrInvalid
	}
	return store.NativeRetentionPolicyHistory(ctx, p.OrgID, source, in)
}

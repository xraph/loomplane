package recovery

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/xraph/loomplane/internal/security"
)

type RetirementListInput struct {
	Before string `json:"before,omitempty" query:"before"`
	Limit  int    `json:"limit,omitempty" query:"limit"`
}

type RetirementPage struct {
	Retirements []Retirement `json:"retirements"`
	NextBefore  string       `json:"next_before,omitempty"`
}

type retirementListCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

func retirementPageLimit(limit int) (int, error) {
	if limit == 0 {
		return 50, nil
	}
	if limit < 1 || limit > 100 {
		return 0, ErrInvalid
	}
	return limit, nil
}

func (in RetirementListInput) bounds() (limit int, before retirementListCursor, err error) {
	limit, err = retirementPageLimit(in.Limit)
	if err != nil {
		return 0, before, err
	}
	if in.Before != "" {
		if len(in.Before) > 512 {
			return 0, before, ErrInvalid
		}
		raw, decodeErr := base64.RawURLEncoding.DecodeString(in.Before)
		if decodeErr != nil || json.Unmarshal(raw, &before) != nil || before.CreatedAt.IsZero() || !requestKey.MatchString(before.ID) {
			return 0, before, ErrInvalid
		}
	}
	return limit, before, nil
}

func (s *Service) ListRetirements(ctx context.Context, p security.Principal, project string, in RetirementListInput) (*RetirementPage, error) {
	if err := s.Authz.Require(ctx, p, security.BackupRead, project); err != nil {
		return nil, err
	}
	if _, err := s.Projects.GetProject(ctx, p.OrgID, project); err != nil {
		return nil, err
	}
	return s.Retirements.ListRetirements(ctx, p.OrgID, project, in)
}

func (s *PgStore) ListRetirements(ctx context.Context, org, project string, in RetirementListInput) (*RetirementPage, error) {
	limit, before, err := in.bounds()
	if err != nil {
		return nil, err
	}
	var timeBound any
	if !before.CreatedAt.IsZero() {
		timeBound = before.CreatedAt
	}
	rows, err := s.db.Query(ctx, `SELECT `+retirementColumns+` FROM loomplane_backup_retirements
 WHERE org_id=$1 AND project_id=$2 AND ($3::timestamptz IS NULL OR (created_at,id)<($3,$4))
 ORDER BY created_at DESC,id DESC LIMIT $5`, org, project, timeBound, before.ID, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &RetirementPage{Retirements: []Retirement{}}
	for rows.Next() {
		r, scanErr := scanRetirement(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		page.Retirements = append(page.Retirements, *r)
	}
	if checkErr := rows.Err(); checkErr != nil {
		return nil, checkErr
	}
	if len(page.Retirements) > limit {
		page.Retirements = page.Retirements[:limit]
		last := page.Retirements[limit-1]
		raw, encodeErr := json.Marshal(retirementListCursor{CreatedAt: last.CreatedAt.UTC(), ID: last.ID})
		if encodeErr != nil {
			return nil, encodeErr
		}
		page.NextBefore = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}

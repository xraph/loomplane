package connections

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

var _ SignInHealthStore = (*PgStore)(nil)

// signInHealthExpr classifies a signed-in connection c, joined to the
// current app a of its cloud (null for a cloud whose grants outlive their
// app). Worst first: nothing works until someone signs in again, then a
// failed check, then missing permissions.
const signInHealthExpr = `CASE
  WHEN c.status = 'sign_in_needed' THEN 'sign_in_needed'
  WHEN a.cloud IS NOT NULL AND c.oauth_client_id <> a.client_id THEN 'app_changed'
  WHEN c.status = 'failed' THEN 'failed'
  WHEN c.status = 'incomplete' THEN 'incomplete'
  ELSE 'healthy' END`

// signedInRows is every signed-in connection with its cloud's current app.
// $1 and $2 are the clouds and client IDs of currentArgs.
const signedInRows = `FROM loomplane_cloud_connections c
  LEFT JOIN unnest($1::text[], $2::text[]) AS a(cloud, client_id) ON a.cloud = c.cloud
  WHERE c.method = 'oauth'`

// currentArgs splits current into the two arrays signedInRows takes.
func currentArgs(current map[string]string) (clouds, clients []string) {
	for _, cloud := range slices.Sorted(maps.Keys(current)) {
		clouds = append(clouds, cloud)
		clients = append(clients, current[cloud])
	}
	return clouds, clients
}

// SignInHealthCounts implements SignInHealthStore.
func (s *PgStore) SignInHealthCounts(ctx context.Context, current map[string]string) (map[string]SignInCounts, error) {
	clouds, clients := currentArgs(current)
	rows, err := s.db.Query(ctx, `SELECT c.cloud, `+signInHealthExpr+` AS health, count(*) `+signedInRows+` GROUP BY 1, 2`, clouds, clients)
	if err != nil {
		return nil, fmt.Errorf("count signed-in connections: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := map[string]SignInCounts{}
	for rows.Next() {
		var cloud, health string
		var n int
		if err := rows.Scan(&cloud, &health, &n); err != nil {
			return nil, fmt.Errorf("scan signed-in connection count: %w", err)
		}
		c := out[cloud]
		c.Add(health, n)
		out[cloud] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count signed-in connections: %w", err)
	}
	return out, nil
}

// SignedInConnections implements SignInHealthStore. Organization names and
// the email of whoever signed in come from authsome's tables.
func (s *PgStore) SignedInConnections(ctx context.Context, current map[string]string, f SignedInFilter) (SignedInPage, error) {
	clouds, clients := currentArgs(current)
	args := []any{clouds, clients}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	where := ""
	if f.Cloud != "" {
		where += " AND c.cloud = " + arg(f.Cloud)
	}
	if f.Search != "" {
		like := arg("%" + escapeLike(f.Search) + "%")
		exact := arg(f.Search)
		where += " AND (c.name ILIKE " + like + " OR c.account ILIKE " + like + " OR o.name ILIKE " + like +
			" OR u.email ILIKE " + like + " OR c.id = " + exact + " OR c.org_id = " + exact + ")"
	}
	base := `FROM (SELECT c.id, c.org_id, COALESCE(o.name, '') AS org_name, c.cloud, c.name, c.account,
    ` + signInHealthExpr + ` AS health, c.status, c.message, c.signed_in_by, COALESCE(u.email, '') AS email,
    c.checked_at, c.created_at, c.updated_at
  FROM loomplane_cloud_connections c
  LEFT JOIN unnest($1::text[], $2::text[]) AS a(cloud, client_id) ON a.cloud = c.cloud
  LEFT JOIN authsome_organizations o ON o.id = c.org_id
  LEFT JOIN authsome_users u ON u.id = c.signed_in_by AND c.signed_in_by <> ''
  WHERE c.method = 'oauth'` + where + `) x`

	out := SignedInPage{Items: []SignedInConnection{}, Limit: f.Limit, Offset: f.Offset}
	rows, err := s.db.Query(ctx, `SELECT x.health, count(*) `+base+` GROUP BY 1`, args...)
	if err != nil {
		return out, fmt.Errorf("count signed-in connections: %w", err)
	}
	for rows.Next() {
		var health string
		var n int
		if err = rows.Scan(&health, &n); err != nil {
			rows.Close()
			return out, fmt.Errorf("scan signed-in connection count: %w", err)
		}
		out.Counts.Add(health, n)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return out, fmt.Errorf("count signed-in connections: %w", err)
	}
	out.Total = out.Counts.Of(f.Health)

	filter := ""
	switch f.Health {
	case "":
	case "problems":
		filter = ` WHERE x.health IN ('sign_in_needed', 'app_changed', 'failed')`
	default:
		filter = ` WHERE x.health = ` + arg(f.Health)
	}
	limit, offset := arg(f.Limit), arg(f.Offset)
	rows, err = s.db.Query(ctx, `SELECT x.id, x.org_id, x.org_name, x.cloud, x.name, x.account, x.health, x.status,
  x.message, x.signed_in_by, x.email, x.checked_at, x.created_at, x.updated_at `+base+filter+`
 ORDER BY CASE x.health WHEN 'sign_in_needed' THEN 0 WHEN 'app_changed' THEN 1 WHEN 'failed' THEN 2
  WHEN 'incomplete' THEN 3 ELSE 4 END, x.updated_at DESC, x.id
 LIMIT `+limit+` OFFSET `+offset, args...)
	if err != nil {
		return out, fmt.Errorf("list signed-in connections: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	for rows.Next() {
		var r SignedInConnection
		var checked *time.Time
		if err := rows.Scan(&r.ID, &r.OrgID, &r.OrgName, &r.Cloud, &r.Name, &r.Account, &r.Health, &r.Status,
			&r.Message, &r.SignedInBy, &r.SignedInByEmail, &checked, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return out, fmt.Errorf("scan signed-in connection: %w", err)
		}
		if checked != nil {
			t := checked.UTC()
			r.CheckedAt = &t
		}
		r.CreatedAt, r.UpdatedAt = r.CreatedAt.UTC(), r.UpdatedAt.UTC()
		out.Items = append(out.Items, r)
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("list signed-in connections: %w", err)
	}
	return out, nil
}

// UserEmails maps each of ids that is still an account to its email.
func (s *PgStore) UserEmails(ctx context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT id, email FROM authsome_users WHERE id = ANY($1::text[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("look up users: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	for rows.Next() {
		var id, email string
		if err := rows.Scan(&id, &email); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		out[id] = email
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("look up users: %w", err)
	}
	return out, nil
}

// escapeLike escapes s for a LIKE pattern, whose default escape is \.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

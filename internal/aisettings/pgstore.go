package aisettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
	"go.jetify.com/typeid/v2"
)

// PgStore is the Postgres Store. The schema is migration group "loomplane"
// (database/migrations).
type PgStore struct {
	db  *pgdriver.PgDB
	now func() time.Time
}

// NewPgStore returns a Store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db, now: time.Now} }

var _ Store = (*PgStore)(nil)

// Money goes in and out as text, so no amount passes through a float.
const settingsColumns = `org_id, monthly_budget_usd::text, embeddings, revision, updated_by, updated_at,
	huggingface_hint, huggingface_account, huggingface_set_at, huggingface_revision`

const huggingFaceColumns = `huggingface_hint, huggingface_account, huggingface_set_at, huggingface_revision`

const keyColumns = `id, org_id, name, type, fields, models, key_hint, projects, revision,
	created_by, updated_by, created_at, updated_at`

const allocationColumns = `scope, project_id, environment_id, label, monthly_budget_usd::text, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanSettings(row scanner) (*Settings, error) {
	var (
		s          Settings
		embeddings []byte
		hf         HuggingFaceToken
		hfSetAt    *time.Time
	)
	if err := row.Scan(&s.OrgID, &s.MonthlyBudgetUSD, &embeddings, &s.Revision, &s.UpdatedBy, &s.UpdatedAt,
		&hf.Hint, &hf.Account, &hfSetAt, &hf.Revision); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(embeddings, &s.Embeddings); err != nil {
		return nil, fmt.Errorf("decode AI embeddings setting: %w", err)
	}
	s.UpdatedAt = s.UpdatedAt.UTC()
	s.HuggingFace = huggingFaceToken(hf, hfSetAt)
	return &s, nil
}

// huggingFaceToken is the token a row records, or nil when its set_at is
// null: none is set.
func huggingFaceToken(hf HuggingFaceToken, setAt *time.Time) *HuggingFaceToken {
	if setAt == nil {
		return nil
	}
	hf.SetAt = setAt.UTC()
	return &hf
}

func scanKey(row scanner) (*ProviderKey, error) {
	var (
		k              ProviderKey
		fields, models []byte
	)
	if err := row.Scan(&k.ID, &k.OrgID, &k.Name, &k.Type, &fields, &models, &k.KeyHint, &k.Projects, &k.Revision,
		&k.CreatedBy, &k.UpdatedBy, &k.CreatedAt, &k.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(fields, &k.Fields); err != nil {
		return nil, fmt.Errorf("decode AI provider key fields: %w", err)
	}
	if err := json.Unmarshal(models, &k.Models); err != nil {
		return nil, fmt.Errorf("decode AI provider key models: %w", err)
	}
	if len(k.Fields) == 0 {
		k.Fields = nil
	}
	if len(k.Models) == 0 {
		k.Models = nil
	}
	if k.Projects == nil {
		k.Projects = []string{}
	}
	k.CreatedAt, k.UpdatedAt = k.CreatedAt.UTC(), k.UpdatedAt.UTC()
	return &k, nil
}

func scanAllocation(row scanner) (*Allocation, error) {
	var a Allocation
	if err := row.Scan(&a.Scope, &a.ProjectID, &a.EnvironmentID, &a.Label, &a.MonthlyBudgetUSD, &a.UpdatedAt); err != nil {
		return nil, err
	}
	a.UpdatedAt = a.UpdatedAt.UTC()
	return &a, nil
}

func rollback(tx driver.Tx) { _ = tx.Rollback() } //nolint:errcheck // rollback is a no-op after commit

// Settings implements Store.
func (s *PgStore) Settings(ctx context.Context, orgID string) (*Settings, error) {
	got, err := scanSettings(s.db.QueryRow(ctx, `SELECT `+settingsColumns+` FROM loomplane_ai_settings WHERE org_id = $1`, orgID))
	if errors.Is(err, pgx.ErrNoRows) {
		return &Settings{OrgID: orgID, MonthlyBudgetUSD: "0.00", Embeddings: LocalEmbeddings()}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get AI settings: %w", err)
	}
	return got, nil
}

// SaveSettings implements Store.
func (s *PgStore) SaveSettings(ctx context.Context, in *Settings, expectRevision int64) error {
	embeddings, err := json.Marshal(in.Embeddings)
	if err != nil {
		return fmt.Errorf("encode AI embeddings setting: %w", err)
	}
	now := s.now().UTC()
	var row driver.Row
	if expectRevision == 0 {
		// Nobody has written this workspace's row: the first writer wins.
		row = s.db.QueryRow(ctx, `INSERT INTO loomplane_ai_settings (org_id, embeddings, updated_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $4)
			ON CONFLICT (org_id) DO NOTHING
			RETURNING `+settingsColumns, in.OrgID, embeddings, in.UpdatedBy, now)
	} else {
		row = s.db.QueryRow(ctx, `UPDATE loomplane_ai_settings
			SET embeddings = $3, updated_by = $4, updated_at = $5, revision = revision + 1
			WHERE org_id = $1 AND revision = $2
			RETURNING `+settingsColumns, in.OrgID, expectRevision, embeddings, in.UpdatedBy, now)
	}
	got, err := scanSettings(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrStale
	}
	if err != nil {
		return fmt.Errorf("save AI settings: %w", err)
	}
	*in = *got
	return nil
}

// ProviderKeys implements Store.
func (s *PgStore) ProviderKeys(ctx context.Context, orgID string) ([]*ProviderKey, error) {
	rows, err := s.db.Query(ctx, `SELECT `+keyColumns+` FROM loomplane_ai_provider_keys WHERE org_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list AI provider keys: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*ProviderKey{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, fmt.Errorf("scan AI provider key: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list AI provider keys: %w", err)
	}
	return out, nil
}

// ProviderKey implements Store.
func (s *PgStore) ProviderKey(ctx context.Context, orgID, name string) (*ProviderKey, error) {
	k, err := scanKey(s.db.QueryRow(ctx, `SELECT `+keyColumns+` FROM loomplane_ai_provider_keys
		WHERE org_id = $1 AND name = $2`, orgID, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get AI provider key: %w", err)
	}
	return k, nil
}

// SaveProviderKey implements Store.
func (s *PgStore) SaveProviderKey(ctx context.Context, k *ProviderKey) error {
	return s.writeProviderKey(ctx, k, `DO UPDATE SET
			type = EXCLUDED.type,
			fields = EXCLUDED.fields,
			models = EXCLUDED.models,
			key_hint = EXCLUDED.key_hint,
			projects = EXCLUDED.projects,
			revision = k.revision + 1,
			updated_by = EXCLUDED.updated_by,
			updated_at = EXCLUDED.updated_at`)
}

// CreateProviderKey implements Store.
func (s *PgStore) CreateProviderKey(ctx context.Context, k *ProviderKey) error {
	return s.writeProviderKey(ctx, k, `DO NOTHING`)
}

// writeProviderKey inserts k, doing onConflict when the workspace already
// has a key with its name. A conflict that writes nothing is ErrStale.
func (s *PgStore) writeProviderKey(ctx context.Context, k *ProviderKey, onConflict string) error {
	id := k.ID
	if id == "" {
		tid, err := typeid.Generate("aikey")
		if err != nil {
			return fmt.Errorf("generate AI provider key id: %w", err)
		}
		id = tid.String()
	}
	fields := k.Fields
	if fields == nil {
		fields = map[string]string{}
	}
	models := k.Models
	if models == nil {
		models = []OpenCompatModel{}
	}
	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("encode AI provider key fields: %w", err)
	}
	modelsJSON, err := json.Marshal(models)
	if err != nil {
		return fmt.Errorf("encode AI provider key models: %w", err)
	}
	projects := k.Projects
	if projects == nil {
		projects = []string{}
	}
	got, err := scanKey(s.db.QueryRow(ctx, `INSERT INTO loomplane_ai_provider_keys AS k
			(id, org_id, name, type, fields, models, key_hint, projects, revision, created_by, updated_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, $9, $10, $11, $11)
		ON CONFLICT (org_id, name) `+onConflict+`
		RETURNING `+keyColumns,
		id, k.OrgID, k.Name, k.Type, fieldsJSON, modelsJSON, k.KeyHint, projects, k.CreatedBy, k.UpdatedBy, s.now().UTC()))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrStale
	}
	if err != nil {
		return fmt.Errorf("save AI provider key: %w", err)
	}
	*k = *got
	return nil
}

// DeleteProviderKey implements Store.
func (s *PgStore) DeleteProviderKey(ctx context.Context, orgID, name string) error {
	res, err := s.db.Exec(ctx, `DELETE FROM loomplane_ai_provider_keys WHERE org_id = $1 AND name = $2`, orgID, name)
	if err != nil {
		return fmt.Errorf("delete AI provider key: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete AI provider key: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Allocations implements Store.
func (s *PgStore) Allocations(ctx context.Context, orgID string) ([]*Allocation, error) {
	rows, err := s.db.Query(ctx, `SELECT `+allocationColumns+` FROM loomplane_ai_budget_allocations
		WHERE org_id = $1 ORDER BY scope`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list AI budget allocations: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Allocation{}
	for rows.Next() {
		a, err := scanAllocation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan AI budget allocation: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list AI budget allocations: %w", err)
	}
	return out, nil
}

// lockSettings makes sure the workspace has a settings row and locks it for
// the rest of tx. Every allocation and budget write takes this lock first,
// so two of them can't both pass their checks against the same total.
func lockSettings(ctx context.Context, tx driver.Tx, orgID string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO loomplane_ai_settings (org_id) VALUES ($1) ON CONFLICT (org_id) DO NOTHING`, orgID); err != nil {
		return fmt.Errorf("create AI settings: %w", err)
	}
	var locked string
	if err := tx.QueryRow(ctx, `SELECT org_id FROM loomplane_ai_settings WHERE org_id = $1 FOR UPDATE`, orgID).Scan(&locked); err != nil {
		return fmt.Errorf("lock AI settings: %w", err)
	}
	return nil
}

// SetAllocation implements Store.
func (s *PgStore) SetAllocation(ctx context.Context, orgID string, a *Allocation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if lockErr := lockSettings(ctx, tx, orgID); lockErr != nil {
		return lockErr
	}
	// What's left counts every other allocation, so raising one counts only
	// its new amount.
	var (
		left string
		over bool
	)
	err = tx.QueryRow(ctx, `SELECT greatest(x.remaining, 0)::numeric(14, 2)::text, $3::numeric(12, 2) > x.remaining
		FROM (SELECT s.monthly_budget_usd - coalesce((SELECT sum(a.monthly_budget_usd) FROM loomplane_ai_budget_allocations a
			WHERE a.org_id = s.org_id AND a.scope <> $2), 0) AS remaining
			FROM loomplane_ai_settings s WHERE s.org_id = $1) x`,
		orgID, a.Scope, a.MonthlyBudgetUSD).Scan(&left, &over)
	if err != nil {
		return fmt.Errorf("check AI budget allocation: %w", err)
	}
	if over {
		return fmt.Errorf("%w: %s USD of it is left to allocate", ErrOverBudget, left)
	}
	got, err := scanAllocation(tx.QueryRow(ctx, `INSERT INTO loomplane_ai_budget_allocations
			(org_id, scope, project_id, environment_id, label, monthly_budget_usd, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6::numeric(12, 2), $7)
		ON CONFLICT (org_id, scope) DO UPDATE SET
			project_id = EXCLUDED.project_id,
			environment_id = EXCLUDED.environment_id,
			label = EXCLUDED.label,
			monthly_budget_usd = EXCLUDED.monthly_budget_usd,
			updated_at = EXCLUDED.updated_at
		RETURNING `+allocationColumns,
		orgID, a.Scope, a.ProjectID, a.EnvironmentID, a.Label, a.MonthlyBudgetUSD, s.now().UTC()))
	if err != nil {
		return fmt.Errorf("save AI budget allocation: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("save AI budget allocation: %w", err)
	}
	*a = *got
	return nil
}

// DeleteAllocation implements Store.
func (s *PgStore) DeleteAllocation(ctx context.Context, orgID, scope string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM loomplane_ai_budget_allocations WHERE org_id = $1 AND scope = $2`, orgID, scope)
	if err != nil {
		return fmt.Errorf("delete AI budget allocation: %w", err)
	}
	return nil
}

// SetBudget implements Store.
func (s *PgStore) SetBudget(ctx context.Context, orgID, budgetUSD, by string) (*Settings, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if lockErr := lockSettings(ctx, tx, orgID); lockErr != nil {
		return nil, lockErr
	}
	var (
		allocated string
		under     bool
	)
	err = tx.QueryRow(ctx, `SELECT x.total::numeric(14, 2)::text, x.total > $2::numeric(12, 2)
		FROM (SELECT coalesce(sum(monthly_budget_usd), 0) AS total FROM loomplane_ai_budget_allocations WHERE org_id = $1) x`,
		orgID, budgetUSD).Scan(&allocated, &under)
	if err != nil {
		return nil, fmt.Errorf("check AI budget: %w", err)
	}
	if under {
		return nil, fmt.Errorf("%w: allocations total %s USD", ErrUnderAllocated, allocated)
	}
	got, err := scanSettings(tx.QueryRow(ctx, `UPDATE loomplane_ai_settings
		SET monthly_budget_usd = $2::numeric(12, 2), updated_by = $3, updated_at = $4, revision = revision + 1
		WHERE org_id = $1
		RETURNING `+settingsColumns, orgID, budgetUSD, by, s.now().UTC()))
	if err != nil {
		return nil, fmt.Errorf("save AI budget: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("save AI budget: %w", err)
	}
	return got, nil
}

// SetHuggingFaceToken implements Store.
func (s *PgStore) SetHuggingFaceToken(ctx context.Context, orgID, hint, account string) (*HuggingFaceToken, error) {
	var (
		hf    HuggingFaceToken
		setAt *time.Time
	)
	err := s.db.QueryRow(ctx, `INSERT INTO loomplane_ai_settings AS s
			(org_id, huggingface_hint, huggingface_account, huggingface_set_at, huggingface_revision)
		VALUES ($1, $2, $3, $4, 1)
		ON CONFLICT (org_id) DO UPDATE SET
			huggingface_hint = EXCLUDED.huggingface_hint,
			huggingface_account = EXCLUDED.huggingface_account,
			huggingface_set_at = EXCLUDED.huggingface_set_at,
			huggingface_revision = s.huggingface_revision + 1
		RETURNING `+huggingFaceColumns, orgID, hint, account, s.now().UTC()).
		Scan(&hf.Hint, &hf.Account, &setAt, &hf.Revision)
	if err != nil {
		return nil, fmt.Errorf("save the Hugging Face token: %w", err)
	}
	return huggingFaceToken(hf, setAt), nil
}

// huggingFaceLockPrefix keys the advisory lock of a workspace's Hugging
// Face token, hashed with the org ID to the lock's 64-bit key.
const huggingFaceLockPrefix = "loomplane-ai-huggingface-token:"

// LockHuggingFaceToken implements Store with a transaction-scoped advisory
// lock. It locks no row, so fn's own writes to the settings row, on other
// connections, don't wait on it.
func (s *PgStore) LockHuggingFaceToken(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("lock the Hugging Face token: %w", err)
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, huggingFaceLockPrefix+orgID); err != nil {
		return fmt.Errorf("lock the Hugging Face token: %w", err)
	}
	if ferr := fn(ctx); ferr != nil {
		return ferr
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("unlock the Hugging Face token: %w", err)
	}
	return nil
}

// ClearHuggingFaceToken implements Store.
func (s *PgStore) ClearHuggingFaceToken(ctx context.Context, orgID string) error {
	_, err := s.db.Exec(ctx, `UPDATE loomplane_ai_settings
		SET huggingface_hint = '', huggingface_account = '', huggingface_set_at = NULL,
			huggingface_revision = huggingface_revision + 1
		WHERE org_id = $1`, orgID)
	if err != nil {
		return fmt.Errorf("clear the Hugging Face token: %w", err)
	}
	return nil
}

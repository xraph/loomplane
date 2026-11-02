package environments

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
)

// SecretVersion identifies the stored secret incarnation and its exact version.
// It contains no value or content hash.
type SecretVersion struct {
	Identity string `json:"identity"`
	Version  int64  `json:"version"`
}

type versionedSecrets interface {
	PutPinned(context.Context, string, string, []byte) (SecretVersion, error)
	Metadata(context.Context, string, string) (SecretVersion, error)
	RevealPinned(context.Context, string, string, SecretVersion) ([]byte, error)
}

type sourceTransactions interface {
	WithSourceTransaction(context.Context, string, func(context.Context) error) error
	PublishSecretVersion(context.Context, *Variable, SecretVersion) error
}

type sourceTransactionKey struct{}
type sourceTransaction struct {
	store *PgStore
	org   string
	tx    driver.Tx
}

type sourceQueries interface {
	Exec(context.Context, string, ...any) (driver.Result, error)
	Query(context.Context, string, ...any) (driver.Rows, error)
	QueryRow(context.Context, string, ...any) driver.Row
}

func (s *PgStore) sourceDB(ctx context.Context) sourceQueries {
	if scope, ok := ctx.Value(sourceTransactionKey{}).(sourceTransaction); ok && scope.store == s {
		return scope.tx
	}
	return s.db
}

// WithSourceTransaction serializes selection, publication and cleanup across
// processes. Vault uses its own database connection while this lock is held.
func (s *PgStore) WithSourceTransaction(ctx context.Context, org string, fn func(context.Context) error) (err error) {
	if scope, ok := ctx.Value(sourceTransactionKey{}).(sourceTransaction); ok {
		if scope.store != s || scope.org != org {
			return ErrInvalid
		}
		return fn(ctx)
	}
	tx, err := s.source.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "variable-sources/v1"); err != nil {
		return err
	}
	if callbackErr := fn(context.WithValue(ctx, sourceTransactionKey{}, sourceTransaction{store: s, org: org, tx: tx})); callbackErr != nil {
		return callbackErr
	}
	return tx.Commit()
}

func (s *PgStore) PublishSecretVersion(ctx context.Context, v *Variable, pin SecretVersion) error {
	scope, ok := ctx.Value(sourceTransactionKey{}).(sourceTransaction)
	if !ok || scope.store != s || scope.org != v.OrgID || !v.Secret || pin.Identity == "" || pin.Version < 1 {
		return ErrInvalid
	}
	res, err := scope.tx.Exec(ctx, `UPDATE loomplane_variables SET secret_identity=$3,secret_version=$4 WHERE org_id=$1 AND id=$2 AND secret AND source_revision=$5`, v.OrgID, v.ID, pin.Identity, pin.Version, v.sourceRevision)
	if err != nil {
		return err
	}
	if rowErr := oneRow(res); rowErr != nil {
		return rowErr
	}
	v.secretIdentity, v.secretVersion = pin.Identity, pin.Version
	return nil
}

func (s *Service) withSourceTransaction(ctx context.Context, org string, fn func(context.Context) error) error {
	if store, ok := s.store.(sourceTransactions); ok {
		return store.WithSourceTransaction(ctx, org, fn)
	}
	// Nonpersistent stores retain the ordinary variable API. Runtime selection
	// explicitly requires sourceTransactions and cannot use this compatibility path.
	return fn(ctx)
}

func (s *Service) enrollSource(ctx context.Context, v *Variable) error {
	if !v.Secret || v.secretIdentity != "" && v.secretVersion > 0 {
		return nil
	}
	store, ok := s.store.(sourceTransactions)
	secrets, versioned := s.secrets.(versionedSecrets)
	if !ok || !versioned {
		return ErrInvalid
	}
	pin, err := secrets.Metadata(ctx, v.OrgID, v.ID)
	if err != nil {
		return err
	}
	return store.PublishSecretVersion(ctx, v, pin)
}

func (s *Service) enrollBeforeMutation(ctx context.Context, org, id string) error {
	if _, ok := s.store.(sourceTransactions); !ok {
		return nil
	}
	return s.withSourceTransaction(ctx, org, func(ctx context.Context) error {
		v, err := s.store.GetVariable(ctx, org, id)
		if err != nil {
			return err
		}
		return s.enrollSource(ctx, v)
	})
}

func (s *Service) putSource(ctx context.Context, v *Variable, value []byte) error {
	if _, ok := s.store.(sourceTransactions); !ok {
		return s.secrets.Put(ctx, v.OrgID, v.ID, value)
	}
	secrets, ok := s.secrets.(versionedSecrets)
	if !ok {
		return ErrInvalid
	}
	pin, err := secrets.PutPinned(ctx, v.OrgID, v.ID, value)
	if err != nil {
		return err
	}
	v.secretIdentity, v.secretVersion = pin.Identity, pin.Version
	return nil
}

func (s *Service) revealSource(ctx context.Context, v *Variable) ([]byte, error) {
	if v.secretIdentity != "" || v.secretVersion != 0 {
		secrets, ok := s.secrets.(versionedSecrets)
		if !ok {
			return nil, ErrInvalid
		}
		return secrets.RevealPinned(ctx, v.OrgID, v.ID, SecretVersion{Identity: v.secretIdentity, Version: v.secretVersion})
	}
	return s.secrets.Reveal(ctx, v.OrgID, v.ID)
}

func (s *Service) cleanupSource(ctx context.Context, org, id string) error {
	return s.withSourceTransaction(ctx, org, func(ctx context.Context) error {
		v, err := s.store.GetVariable(ctx, org, id)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if err == nil && v.Secret {
			return nil
		}
		return s.secrets.Delete(ctx, org, id)
	})
}

func sameSourceScope(a, b *Variable) bool {
	return a.ID == b.ID && a.OrgID == b.OrgID && a.ProjectID == b.ProjectID && a.environmentID == b.environmentID && a.Environment == b.Environment && a.Scope == b.Scope && a.Service == b.Service && a.Key == b.Key
}

func (s *Service) validateSourceScope(ctx context.Context, v *Variable) error {
	if store, ok := s.store.(*PgStore); ok {
		return store.validateSourceScope(ctx, v)
	}
	return nil
}
func (s *PgStore) validateSourceScope(ctx context.Context, v *Variable) error {
	if v.ProjectID == "" {
		return nil
	}
	var exists bool
	err := s.sourceDB(ctx).QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_projects p WHERE p.org_id=$1 AND p.id=$2 AND ($3='' OR EXISTS(SELECT 1 FROM loomplane_environments e WHERE e.org_id=p.org_id AND e.project_id=p.id AND e.id=$3 AND e.name=$4 AND NOT EXISTS(SELECT 1 FROM loomplane_fleet_instances i WHERE i.org_id=e.org_id AND i.environment_id=e.id))))`, v.OrgID, v.ProjectID, v.environmentID, v.Environment).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return ErrSourceSelection
	}
	return nil
}

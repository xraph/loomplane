package environments

import (
	"context"

	"github.com/xraph/grove/driver"
)

// LockRuntimeSourcesTx precedes Cloud, parent-row and quota locks. It must not
// be called from a transaction which already holds those locks.
func LockRuntimeSourcesTx(ctx context.Context, tx driver.Tx) error {
	if tx == nil {
		return ErrSourceSelection
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('variable-sources/v1',0))`)
	return err
}

// ValidateRuntimeSelectionTx checks the complete selected metadata, including
// empty selections and precedence, on the caller's transaction connection.
// The caller holds LockRuntimeSourcesTx. No Vault or authorization call occurs.
func ValidateRuntimeSelectionTx(ctx context.Context, tx driver.Tx, selection RuntimeSelection) error {
	if tx == nil || selection.SchemaVersion != 1 || selection.OrgID == "" || selection.ProjectID == "" || selection.EnvironmentID == "" || selection.Revision == "" {
		return ErrSourceSelection
	}
	store := &PgStore{}
	scoped := context.WithValue(ctx, sourceTransactionKey{}, sourceTransaction{store: store, org: selection.OrgID, tx: tx})
	if err := store.validateSourceScope(scoped, &Variable{OrgID: selection.OrgID, ProjectID: selection.ProjectID, environmentID: selection.EnvironmentID, Environment: selection.Environment}); err != nil {
		return err
	}
	candidates, err := store.ForEnvironment(scoped, selection.OrgID, selection.ProjectID, selection.EnvironmentID, selection.Environment)
	if err != nil {
		return err
	}
	current := selection
	current.Sources = []RuntimeSourcePin{}
	for _, winner := range resolve(candidates, selection.Service) {
		v := winner.Variable
		if v.Secret && (v.secretIdentity == "" || v.secretVersion < 1) {
			return ErrSourceSelection
		}
		current.Sources = append(current.Sources, sourcePin(&v))
	}
	digest, err := selectionRevision(current)
	if err != nil || digest != selection.Revision {
		return ErrSourceSelection
	}
	expected, err := selectionRevision(selection)
	if err != nil || expected != selection.Revision {
		return ErrSourceSelection
	}
	return nil
}

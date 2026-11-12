package adoptiontransfer

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
)

var ErrNotFound = errors.New("transfer journal not found")

// AcceptanceReader reloads a persisted, reviewed transfer under its original
// actor and scope. It must reject revoked, expired or unsupported acceptance.
// There is no production implementation until adoption authority is qualified.
type AcceptanceReader interface {
	ReadTransferAcceptance(context.Context, string, string) (Intent, error)
}

// ReceiptReader resolves immutable evidence by reference through the domain that
// owns its provenance. A request body must never implement this interface.
type ReceiptReader interface {
	ReadTransferReceipt(context.Context, Intent, string) (Receipt, error)
}

// PgStore records evidence and retains physical-source exclusion through
// cancellation. Its methods cannot install credentials or activate ownership.
type PgStore struct {
	db         *pgdriver.PgDB
	acceptance AcceptanceReader
	receipts   ReceiptReader
}

func NewPgStore(db *pgdriver.PgDB, acceptance AcceptanceReader, receipts ReceiptReader) *PgStore {
	return &PgStore{db: db, acceptance: acceptance, receipts: receipts}
}

// Accept reserves an evidence journal from trusted acceptance. It does not
// create a fleet instance, dispatch remote effects or reserve runtime capacity.
func (s *PgStore) Accept(ctx context.Context, org, id string) (result Journal, err error) {
	if !validID(org) || !validID(id) {
		return Journal{}, ErrInvalid
	}
	if s.acceptance == nil {
		return Journal{}, ErrQualificationRequired
	}
	intent, err := s.acceptance.ReadTransferAcceptance(ctx, org, id)
	if err != nil {
		return Journal{}, err
	}
	if intent.Fields().OrgID != org || intent.Fields().ID != id {
		return Journal{}, ErrInvalid
	}
	if intent.Fields().Direction != Forward {
		return Journal{}, ErrQualificationRequired
	}
	initial, err := NewJournal(intent)
	if err != nil {
		return Journal{}, err
	}
	if s.db == nil {
		return Journal{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Journal{}, err
	}
	defer func() { err = finishTx(tx, err) }()
	f := intent.Fields()
	raw, err := json.Marshal(f)
	if err != nil || len(raw) > 65536 {
		return Journal{}, ErrInvalid
	}
	// The unique source key is global across fleet and connection aliases.
	// No source claim can be released by a journal cancellation or lease expiry.
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_transfer_journals(id,org_id,project_id,fleet_id,operation_id,source_key,intent,intent_digest,revision,head_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,1,$9) ON CONFLICT(id) DO NOTHING`, f.ID, f.OrgID, f.ProjectID, f.FleetID, f.OperationID, SourceKey(f.Source), string(raw), intent.Digest(), initial.digest)
	if err != nil {
		return Journal{}, mapStoreError(err)
	}
	result, err = loadJournal(ctx, tx, org, id)
	if err != nil {
		return Journal{}, err
	}
	if result.State().Intent().Digest() != intent.Digest() {
		return Journal{}, ErrConflict
	}
	err = tx.Commit()
	return result, err
}

func (s *PgStore) Get(ctx context.Context, org, id string) (result Journal, err error) {
	if s.db == nil || !validID(org) || !validID(id) {
		return Journal{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Journal{}, err
	}
	defer func() { err = finishTx(tx, err) }()
	result, err = loadJournal(ctx, tx, org, id)
	if err != nil {
		return Journal{}, err
	}
	err = tx.Commit()
	return result, err
}

// Apply accepts worker bookkeeping only. Use Record for trusted receipt lookup.
func (s *PgStore) Apply(ctx context.Context, org, id string, c Command) (Journal, error) {
	if c.Kind == Record {
		return Journal{}, ErrReceipt
	}
	return s.append(ctx, org, id, c)
}

// Record loads the receipt outside the database transaction, then rechecks the
// original revision and lease under the journal lock before appending it.
func (s *PgStore) Record(ctx context.Context, org, id string, revision uint64, lease, reference string) (Journal, error) {
	if s.receipts == nil {
		return Journal{}, ErrQualificationRequired
	}
	journal, err := s.Get(ctx, org, id)
	if err != nil {
		return Journal{}, err
	}
	if journal.State().Revision() != revision {
		return Journal{}, ErrConflict
	}
	receipt, err := ResolveReceipt(ctx, s.receipts, journal.State(), reference, time.Now().UTC())
	if err != nil {
		return Journal{}, err
	}
	return s.append(ctx, org, id, Command{Kind: Record, ExpectedRevision: revision, LeaseToken: lease, Receipt: receipt})
}

func (s *PgStore) append(ctx context.Context, org, id string, c Command) (result Journal, err error) {
	if c.Kind == Activate || c.Kind == ReverseTransfer {
		return Journal{}, ErrQualificationRequired
	}
	if s.db == nil || !validID(org) || !validID(id) {
		return Journal{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Journal{}, err
	}
	defer func() { err = finishTx(tx, err) }()
	current, err := loadJournal(ctx, tx, org, id)
	if err != nil {
		return Journal{}, err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return Journal{}, err
	}
	result, err = current.Apply(c, now)
	if err != nil {
		return Journal{}, err
	}
	if result.State().Revision() == current.State().Revision() {
		err = tx.Commit()
		return result, err
	}
	entry := result.entries[len(result.entries)-1]
	payload, err := json.Marshal(entry)
	if err != nil || len(payload) > 65536 {
		return Journal{}, ErrInvalid
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_transfer_entries(transfer_id,revision,payload) VALUES($1,$2,$3)`, id, int64(result.State().Revision()), string(payload)); err != nil {
		return Journal{}, mapStoreError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_fleet_transfer_journals SET revision=$3,head_digest=$4 WHERE id=$1 AND org_id=$2`, id, org, int64(result.State().Revision()), result.digest); err != nil {
		return Journal{}, err
	}
	err = tx.Commit()
	return result, err
}

func loadJournal(ctx context.Context, tx driver.Tx, org, id string) (Journal, error) {
	var raw []byte
	var revision int64
	var digest, intentDigest, sourceKey, project, fleet, operation string
	err := tx.QueryRow(ctx, `SELECT intent,revision,head_digest,intent_digest,source_key,project_id,fleet_id,operation_id FROM loomplane_fleet_transfer_journals WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, id).Scan(&raw, &revision, &digest, &intentDigest, &sourceKey, &project, &fleet, &operation)
	if err != nil {
		return Journal{}, mapStoreError(err)
	}
	if revision < 1 || len(raw) > 65536 {
		return Journal{}, ErrInvalid
	}
	var intent IntentFields
	if err = decodeStrict(raw, &intent); err != nil {
		return Journal{}, ErrInvalid
	}
	checked, err := NewIntent(intent)
	if err != nil || checked.Digest() != intentDigest || intent.OrgID != org || intent.ID != id || intent.ProjectID != project || intent.FleetID != fleet || intent.OperationID != operation || SourceKey(intent.Source) != sourceKey {
		return Journal{}, ErrInvalid
	}
	journal, err := NewJournal(checked)
	if err != nil {
		return Journal{}, err
	}
	var last int64 = 1
	for {
		if err = ctx.Err(); err != nil {
			return Journal{}, err
		}
		rows, queryErr := tx.Query(ctx, `SELECT revision,payload FROM loomplane_fleet_transfer_entries WHERE transfer_id=$1 AND revision>$2 ORDER BY revision LIMIT 128`, id, last)
		if queryErr != nil {
			return Journal{}, queryErr
		}
		count := 0
		for rows.Next() {
			var sequence int64
			var payload []byte
			if err = rows.Scan(&sequence, &payload); err != nil {
				rows.Close()
				return Journal{}, err
			}
			if sequence != last+1 || sequence > revision || len(payload) > 65536 {
				rows.Close()
				return Journal{}, ErrInvalid
			}
			var entry journalEntry
			if err = decodeStrict(payload, &entry); err != nil {
				rows.Close()
				return Journal{}, ErrInvalid
			}
			command, decodeErr := entry.Command.decode()
			if decodeErr != nil {
				rows.Close()
				return Journal{}, decodeErr
			}
			next, applyErr := journal.Apply(command, entry.At)
			if applyErr != nil || next.State().Revision() != uint64(sequence) || entryDigest(journal.digest, entry) != next.digest {
				rows.Close()
				return Journal{}, ErrInvalid
			}
			journal = next
			last = sequence
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return Journal{}, err
		}
		if count < 128 {
			break
		}
	}
	if last != revision || journal.digest != digest {
		return Journal{}, ErrInvalid
	}
	return journal, nil
}

func finishTx(tx driver.Tx, err error) error {
	if err == nil {
		return nil
	}
	rollbackErr := tx.Rollback()
	if rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
		return errors.Join(err, rollbackErr)
	}
	return err
}
func mapStoreError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505":
			return ErrConflict
		case "23503":
			return ErrNotFound
		case "23514":
			return ErrInvalid
		}
	}
	return err
}

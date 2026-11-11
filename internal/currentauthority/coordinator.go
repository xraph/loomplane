// Package currentauthority owns bounded transactions for fresh authorization.
// The installed identity profile must be verified before a coordinator can run.
package currentauthority

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/observability"
	"github.com/xraph/warden/store"

	"github.com/xraph/loomplane/internal/security"
)

const (
	AttemptLimit  = 5 * time.Second
	CleanupLimit  = time.Second
	LockWaitLimit = 2 * time.Second
)

var (
	ErrUnavailable   = errors.New("current authority is unavailable")
	ErrExpired       = errors.New("current authority scope has expired")
	ErrBusy          = errors.New("current authority still owns transactions")
	ErrPoisoned      = errors.New("current authority scope failed")
	ErrLifecycle     = errors.New("only the authority coordinator may finalize a transaction")
	ErrBinding       = errors.New("current authority binding does not match")
	ErrIncomplete    = errors.New("current authority operation was not staged")
	ErrPending       = errors.New("current authority has unfinished SQL or evaluation")
	ErrDecision      = errors.New("current authority decision is invalid or already used")
	ErrTemporal      = errors.New("authority changed across the evaluation interval")
	ErrCommitUnknown = errors.New("authority commit outcome requires reconciliation")
)

// Binding names one immutable operation. InputDigest is a lowercase SHA-256
// digest of the domain's canonical input. RecordID and AttemptID must also be
// checked against durable rows by the staging adapter.
//
// Binding is the ordinary writable-user entry point. Fleet service credentials
// use FleetBinding and the fixed Fleet evaluator.
type Binding struct {
	Actor       security.Principal
	AppID       string
	Operation   string
	InputDigest string
	RecordID    string
	AttemptID   string
}

type binding struct {
	kind                                                    security.Kind
	user, org, key, app, operation, digest, record, attempt string
	project, fleet                                          string
}

func canonical(b Binding) (binding, error) {
	p := b.Actor
	if p.Kind != security.KindUser || p.Fleet != nil || p.OnBehalfOf != nil || p.ReadOnly ||
		p.UserID == "" || p.OrgID == "" || strings.HasPrefix(p.UserID, "loomplane:") ||
		strings.HasPrefix(p.UserID, "agent:") || security.IsFleetSubject(p.UserID) {
		return binding{}, ErrBinding
	}
	return canonicalOperation(b)
}

func canonicalOperation(b Binding) (binding, error) {
	p := b.Actor
	if b.AppID == "" || b.Operation == "" || b.RecordID == "" || b.AttemptID == "" || len(b.InputDigest) != 64 || strings.ToLower(b.InputDigest) != b.InputDigest {
		return binding{}, ErrBinding
	}
	if _, err := hex.DecodeString(b.InputDigest); err != nil {
		return binding{}, ErrBinding
	}
	return binding{kind: p.Kind, user: p.UserID, org: p.OrgID, key: p.KeyID, app: b.AppID, operation: b.Operation, digest: b.InputDigest, record: b.RecordID, attempt: b.AttemptID}, nil
}

type phase uint8

const (
	evaluating phase = iota
	sealed
	finalizing
	finished
)

type scopeKey struct{}

// Coordinator owns transaction acquisition, locks, sealing and finalization.
// A nil or zero coordinator is unavailable. Fixed startup installation qualifies
// the database and engine before publishing an execution profile.
type Coordinator struct {
	mu            sync.Mutex
	installing    bool
	installCancel context.CancelFunc
	installation  *builtInInstallation
	backend       backend
	reader        store.Store
	schema        string
	source        func(context.Context, driver.Tx, Binding) error
	telemetry     *observability.Telemetry
	active        map[*Attempt]struct{}
	changed       chan struct{}
	stopping      bool
	closed        bool
	closing       bool
	closeDone     chan struct{}
	cleanupErr    error // First late cleanup failure; never an unbounded history.
	profile       *profile
	now           func() time.Time
}

// Attempt has no native connection or transaction lifecycle access. You must
// finish all SQL and stage exactly one record before the callback returns.
type Attempt struct {
	owner      *Coordinator
	schema     string
	binding    binding
	profile    *profile
	ctx        context.Context
	cancel     context.CancelFunc
	tx         nativeTx
	handle     *queryTx
	phase      phase
	poison     error
	pending    int
	fenced     bool
	rows       map[*scopedRows]struct{}
	gate       chan struct{}
	evaluation bool
	decision   *Decision
	staging    bool
	staged     bool
}

// Decision is bound to one attempt and one operation. Stage consumes it once.
type decisionKind uint8

const (
	mechanismDecision decisionKind = iota
	ordinaryDecision
	fleetDecision
)

type Decision struct {
	kind     decisionKind
	ordinary *ordinaryProof
	fleet    *fleetProof
	attempt  *Attempt
	binding  binding
	at       time.Time
	used     bool
}

// Tx returns a compatibility handle for existing guards. Commit and Rollback
// refuse and poison the attempt. There is no native connection escape hatch.
func (a *Attempt) Tx() driver.Tx { return a.handle }

// Run returns nil only after a known successful native commit. Keep captured
// results private until then. On ErrCommitUnknown, reconcile the same AttemptID
// and RecordID; don't create a replacement capability or call a provider.
// Work must use the supplied context/transaction and do no provider or Vault I/O.
func (c *Coordinator) Run(ctx context.Context, b Binding, work func(context.Context, *Attempt) error) (err error) {
	if c == nil {
		return ErrUnavailable
	}
	bound, err := canonical(b)
	if err != nil {
		return err
	}
	return c.run(ctx, bound, work)
}

func (c *Coordinator) run(ctx context.Context, bound binding, work func(context.Context, *Attempt) error) (err error) {
	if c == nil {
		return ErrUnavailable
	}
	if work == nil {
		return ErrIncomplete
	}
	c.mu.Lock()
	if c.installing || !c.profile.supports(bound.kind) || c.backend == nil || c.source == nil || c.schema == "" || c.stopping || c.closed {
		c.mu.Unlock()
		return ErrUnavailable
	}
	if bound.kind == security.KindFleet && (c.profile.fleet.appID == "" || bound.app != c.profile.fleet.appID) {
		c.mu.Unlock()
		return ErrBinding
	}
	ctx, cancel := context.WithTimeout(ctx, AttemptLimit)
	a := &Attempt{owner: c, schema: c.schema, binding: bound, profile: c.profile, cancel: cancel, rows: make(map[*scopedRows]struct{}), gate: make(chan struct{}, 1)}
	a.gate <- struct{}{}
	a.ctx = context.WithValue(ctx, scopeKey{}, a)
	a.handle = &queryTx{attempt: a}
	if c.active == nil {
		c.active = make(map[*Attempt]struct{})
	}
	c.active[a] = struct{}{}
	c.mu.Unlock()
	committed := false
	defer func() {
		cancel()
		if !committed && a.tx != nil {
			c.mu.Lock()
			a.phase = finalizing
			c.mu.Unlock()
			cleanup, stop := context.WithTimeout(context.Background(), CleanupLimit)
			// Scoped pgx calls carry the attempt deadline. If a broken driver outlives
			// cleanup, keep ownership registered until it drains; never close its pool.
			select {
			case <-a.gate:
				err = errors.Join(err, a.rollback(cleanup))
				stop()
				c.release(a)
			case <-cleanup.Done():
				err = errors.Join(err, cleanup.Err())
				stop()
				go func() {
					<-a.gate
					// Reuse the expired cleanup budget. Native rollback closes an
					// unusable connection; it must not get another waiting period.
					if rollbackErr := a.rollback(cleanup); rollbackErr != nil {
						c.mu.Lock()
						if c.cleanupErr == nil {
							c.cleanupErr = fmt.Errorf("late authority cleanup failed: %w", rollbackErr)
						}
						c.mu.Unlock()
					}
					c.release(a)
				}()
			}
		} else {
			c.release(a)
		}
	}()
	a.tx, err = c.backend.begin(a.ctx)
	if err != nil {
		return fmt.Errorf("%w: begin: %w", ErrUnavailable, err)
	}
	if setupErr := a.setup(a.ctx); setupErr != nil {
		return setupErr
	}
	if sourceErr := c.source(a.ctx, a.handle, publicBinding(bound)); sourceErr != nil {
		return sourceErr
	}
	if _, err = a.handle.Exec(a.ctx, fenceSQL(c.schema)); err != nil {
		return err
	}
	c.mu.Lock()
	a.fenced = true
	c.mu.Unlock()
	if workErr := work(a.ctx, a); workErr != nil {
		return workErr
	}
	c.mu.Lock()
	err = a.validLocked(a.ctx)
	if err == nil {
		err = a.profile.verify()
		if err != nil {
			a.poison = errors.Join(a.poison, err)
		}
	}
	if err == nil && (a.pending != 0 || a.evaluation || a.staging) {
		err = ErrPending
	}
	if err == nil && !a.staged {
		err = ErrIncomplete
	}
	if err == nil {
		a.phase = sealed
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	// Seal and Close share the coordinator lock. Calls after this point expire;
	// they cannot revoke a commit already sent to PostgreSQL.
	commitCtx, op := c.telemetry.StartSQL(a.ctx, "COMMIT")
	err = a.tx.Commit(commitCtx)
	op.End(err)
	if err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return fmt.Errorf("%w: %w", ErrPoisoned, err)
		}
		return fmt.Errorf("%w (%s): %w", ErrCommitUnknown, bound.attempt, err)
	}
	committed = true
	return nil
}

func (a *Attempt) setup(ctx context.Context) error {
	if _, err := a.handle.Exec(ctx, `SELECT set_config('lock_timeout',$1,true), set_config('statement_timeout',$2,true), set_config('idle_in_transaction_session_timeout',$2,true)`, "2000ms", "5000ms"); err != nil {
		return err
	}
	var replicationRole string
	if err := a.handle.QueryRow(ctx, `SELECT pg_catalog.current_setting('session_replication_role')`).Scan(&replicationRole); err != nil {
		return err
	}
	if replicationRole != "origin" {
		return ErrUnavailable
	}
	return nil
}

func (a *Attempt) rollback(ctx context.Context) error {
	for row := range a.rows {
		row.closeNative()
	}
	ctx, op := a.owner.telemetry.StartSQL(ctx, "ROLLBACK")
	err := a.tx.Rollback(ctx)
	if errors.Is(err, pgx.ErrTxClosed) {
		err = nil
	}
	op.End(err)
	return err
}

func (c *Coordinator) release(a *Attempt) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a.phase = finished
	delete(c.active, a)
	if c.changed != nil {
		close(c.changed)
		c.changed = nil
	}
}

func (a *Attempt) validLocked(ctx context.Context) error {
	if a.phase != evaluating {
		return ErrExpired
	}
	if ctx.Value(scopeKey{}) != a {
		a.poison = errors.Join(a.poison, ErrBinding)
	}
	if err := a.ctx.Err(); err != nil {
		a.poison = errors.Join(a.poison, err)
	}
	if err := ctx.Err(); err != nil {
		a.poison = errors.Join(a.poison, err)
	}
	if a.profile != a.owner.profile || a.owner.stopping {
		a.poison = errors.Join(a.poison, ErrUnavailable)
	}
	if a.poison != nil {
		return errors.Join(ErrPoisoned, a.poison)
	}
	return nil
}

func (a *Attempt) fail(err error) error {
	a.owner.mu.Lock()
	defer a.owner.mu.Unlock()
	if a.phase != evaluating {
		return ErrExpired
	}
	a.poison = errors.Join(a.poison, err)
	return err
}

// Close refuses while an attempt owns resources. Before seal it also poisons
// the attempt. During commit it refuses without claiming to undo that commit.
// Once drained, repeated Close calls retain the first late cleanup failure.
func (c *Coordinator) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.installing {
		c.mu.Unlock()
		return ErrBusy
	}
	if len(c.active) > 0 {
		for a := range c.active {
			if a.phase == evaluating {
				a.poison = errors.Join(a.poison, ErrBusy)
				a.cancel()
			}
		}
		c.mu.Unlock()
		return ErrBusy
	}
	if c.closing {
		c.mu.Unlock()
		return ErrBusy
	}
	if c.closed {
		closeErr := c.cleanupErr
		c.mu.Unlock()
		return closeErr
	}
	c.closed, c.closing = true, true
	c.closeDone = make(chan struct{})
	ownedBackend := c.backend
	c.mu.Unlock()
	return c.finishClose(ownedBackend)
}

// Driver teardown can outlive the caller's shutdown budget. Keep it owned and
// let later Shutdown calls join the same cleanup operation.
func (c *Coordinator) finishClose(ownedBackend backend) error {
	var closeErr error
	if ownedBackend != nil {
		closeErr = ownedBackend.close()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupErr = errors.Join(c.cleanupErr, closeErr)
	c.closing = false
	close(c.closeDone)
	return c.cleanupErr
}

// Shutdown stops admission and drains evaluation and finalization before closing
// pools. A timeout leaves resources owned; you can call Shutdown again.
func (c *Coordinator) Shutdown(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	c.stopping = true
	if c.installCancel != nil {
		c.installCancel()
	}
	for a := range c.active {
		if a.phase == evaluating {
			a.poison = errors.Join(a.poison, ErrUnavailable)
			a.cancel()
		}
	}
	for len(c.active) > 0 || c.installing {
		if c.changed == nil {
			c.changed = make(chan struct{})
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
		c.mu.Lock()
	}
	if !c.closing && c.closed {
		err := c.cleanupErr
		c.mu.Unlock()
		return err
	}
	if !c.closing {
		c.closed, c.closing = true, true
		c.closeDone = make(chan struct{})
		ownedBackend := c.backend
		go func() { _ = c.finishClose(ownedBackend) }()
	}
	done := c.closeDone
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		c.mu.Lock()
		err := c.cleanupErr
		c.mu.Unlock()
		return err
	}
}

// Drained reports that owned attempts, installation and driver cleanup have all
// finished. A completed cleanup may still return diagnostics from Shutdown.
// Once true, this coordinator cannot install or admit work again.
func (c *Coordinator) Drained() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed && !c.closing && !c.installing && len(c.active) == 0
}

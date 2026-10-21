package agent

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// SnapshotReceiver connects signed policy to one freshly enrolled agent Store.
// Wrappers share the Store's replay floors and cannot replace its trust binding.
type SnapshotReceiver struct {
	state    *Store
	identity domain.AgentIdentity
	trust    *access.SnapshotTrust
}

// NewSnapshotReceiver may run before enrollment; Apply and Open remain closed
// until Activate succeeds. Only authenticated enrollment may call Activate.
func NewSnapshotReceiver(state *Store, identity domain.AgentIdentity, trust *access.SnapshotTrust) (*SnapshotReceiver, error) {
	parsed, err := workloadidentity.ParseAgentURI(identity.URI)
	if state == nil || trust == nil || err != nil || parsed != identity {
		return nil, ErrClosed
	}
	return &SnapshotReceiver{state: state, identity: identity, trust: trust}, nil
}

// Apply implements Agent.Receive after signature and durable epoch checks.
func (r *SnapshotReceiver) Apply(ctx context.Context, snapshot domain.Snapshot) (domain.Acknowledgment, error) {
	if err := ctx.Err(); err != nil {
		return domain.Acknowledgment{}, err
	}
	verified, err := r.trust.Verify(snapshot, r.identity, time.Now())
	if err != nil {
		return domain.Acknowledgment{}, err
	}
	s := r.state
	s.mu.Lock()
	defer s.mu.Unlock()
	var ack domain.Acknowledgment
	err = s.withEpochLocked(ctx, r.identity, func(operation context.Context) error {
		if s.policyTrust != nil && s.policyTrust != r.trust {
			return ErrClosed
		}
		if s.policy == nil {
			policy, createErr := access.NewPolicyState(r.identity)
			if createErr != nil {
				return createErr
			}
			s.policy = policy
			s.policyTrust = r.trust
		}
		var applyErr error
		ack, applyErr = s.policy.Apply(operation, verified)
		return applyErr
	})
	if err != nil {
		return domain.Acknowledgment{}, err
	}
	if s.policyStop == nil {
		stop := make(chan struct{})
		s.policyStop = stop
		policy := s.policy
		interval := time.Second
		if s.policyCheckInterval > 0 {
			interval = s.policyCheckInterval
		}
		s.policyWatchers.Go(func() { s.watchPolicyEpoch(policy, stop, interval) })
	}
	return ack, nil
}

// Open rechecks the durable enrollment before authorizing a session. Gateways
// must derive the request from verified identity and honor session cancellation.
func (r *SnapshotReceiver) Open(ctx context.Context, request access.Request) (*access.Session, error) {
	s := r.state
	s.mu.Lock()
	defer s.mu.Unlock()
	var session *access.Session
	err := s.withEpochLocked(ctx, r.identity, func(operation context.Context) error {
		if s.policy == nil || s.policyTrust != r.trust {
			return access.ErrDenied
		}
		var openErr error
		// The database operation is bounded; the session follows the caller's context.
		if err := operation.Err(); err != nil {
			return err
		}
		session, openErr = s.policy.Open(ctx, request)
		return openErr
	})
	if err != nil {
		return nil, err
	}
	return session, nil
}

// withEpochLocked serializes admission against another process's Activate.
// A failed commit closes all local policy before an acknowledgment or session
// can escape. Caller cancellation before starting leaves current policy intact.
func (s *Store) withEpochLocked(ctx context.Context, identity domain.AgentIdentity, fn func(context.Context) error) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.active || s.identity != identity {
		return ErrClosed
	}
	operation, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(operation, nil)
	if err != nil {
		s.deactivateLocked()
		return errors.Join(ErrClosed, err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			s.deactivateLocked()
			resultErr = errors.Join(ErrClosed, resultErr, rollbackErr)
		}
	}()
	var epoch int64
	err = tx.QueryRow(operation, `UPDATE agent_state SET epoch=epoch WHERE id=1 AND epoch=? AND identity_uri=? RETURNING epoch`, s.epoch, s.identity.URI).Scan(&epoch)
	if err != nil {
		s.deactivateLocked()
		return errors.Join(ErrClosed, err)
	}
	if applyErr := fn(operation); applyErr != nil {
		return applyErr
	}
	if err = tx.Commit(); err != nil {
		s.deactivateLocked()
		return errors.Join(ErrClosed, err)
	}
	return nil
}
func (s *Store) deactivateLocked() {
	s.active = false
	if s.policyStop != nil {
		close(s.policyStop)
		s.policyStop = nil
	}
	if s.policy != nil {
		s.policy.Close()
		s.policy = nil
	}
	s.policyTrust = nil
}
func (s *Store) watchPolicyEpoch(policy *access.PolicyState, stop <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		s.mu.Lock()
		if s.policy != policy || !s.active {
			s.mu.Unlock()
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var epoch int64
		var uri string
		err := s.db.QueryRow(ctx, `SELECT epoch,identity_uri FROM agent_state WHERE id=1`).Scan(&epoch, &uri)
		cancel()
		if err != nil || epoch != s.epoch || uri != s.identity.URI {
			s.deactivateLocked()
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
	}
}

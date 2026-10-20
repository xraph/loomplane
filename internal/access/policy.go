package access

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

var (
	ErrDenied       = errors.New("federation access denied")
	ErrPolicyReplay = errors.New("federation policy sequence or generation is stale")
)

// Request must be derived from authenticated workload identity and the actual
// upstream request. User headers cannot supply Source or IdentityProfile.
// Authorization alone does not establish endpoint freshness or route identity.
type Request struct {
	Source          domain.WorkloadSelector
	Target          domain.WorkloadSelector
	ExportID        string
	Port            uint16
	Protocol        domain.Protocol
	IdentityProfile domain.IdentityProfile
	HTTPMethod      string
	HTTPPath        string
	GRPCMethod      string
}

// Session carries cancellation for the gateway to terminate an active stream.
// The gateway must close its upstream connection when Context is canceled.
type Session struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	owner  *PolicyState
}

func (s *Session) Context() context.Context { return s.ctx }
func (s *Session) Close()                   { s.owner.remove(s) }

type installedPolicy struct {
	verified VerifiedSnapshot
	snapshot domain.Snapshot
	stop     chan struct{}
}

// PolicyState owns one process's authorization. Restart requires a fresh
// enrollment epoch before construction; persisted snapshots cannot activate it.
// Close is terminal and must be coupled to the agent enrollment lifecycle.
type PolicyState struct {
	mu         sync.Mutex
	identity   domain.AgentIdentity
	current    *installedPolicy
	sequence   uint64
	generation uint64
	revision   string
	closed     bool
	sessions   map[*Session]Request
	watchers   sync.WaitGroup
}

func NewPolicyState(identity domain.AgentIdentity) (*PolicyState, error) {
	parsed, err := workloadidentity.ParseAgentURI(identity.URI)
	if err != nil || parsed != identity {
		return nil, ErrDenied
	}
	return &PolicyState{identity: identity, sessions: make(map[*Session]Request)}, nil
}

// Apply validates all rules before replacing current state under one lock.
// Invalid updates preserve only the previous still-valid policy.
func (p *PolicyState) Apply(ctx context.Context, verified VerifiedSnapshot) (domain.Acknowledgment, error) {
	denied := domain.Acknowledgment{}
	if err := ctx.Err(); err != nil {
		return denied, err
	}
	snapshot, err := verified.Snapshot()
	if err != nil {
		return denied, err
	}
	if snapshot.Scope != p.identity.Scope || snapshot.DestinationID != p.identity.DestinationID || snapshot.Epoch != p.identity.Epoch {
		return denied, ErrDenied
	}
	if err := validateCompiledPolicy(snapshot); err != nil {
		return denied, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if err := ctx.Err(); err != nil {
		return denied, err
	}
	if p.closed {
		return denied, ErrDenied
	}
	if err := verified.ValidAt(now); err != nil {
		return denied, err
	}
	if snapshot.Sequence <= p.sequence || snapshot.Policy.Generation < p.generation || (snapshot.Policy.Generation == p.generation && snapshot.Policy.Revision != p.revision) {
		return denied, ErrPolicyReplay
	}
	if p.current != nil {
		close(p.current.stop)
	}
	next := &installedPolicy{verified: verified, snapshot: snapshot, stop: make(chan struct{})}
	p.current = next
	p.sequence = snapshot.Sequence
	p.generation = snapshot.Policy.Generation
	p.revision = snapshot.Policy.Revision
	for session, request := range p.sessions {
		if !permits(snapshot.Policy, request) {
			delete(p.sessions, session)
			session.cancel(ErrDenied)
		}
	}
	p.watchers.Go(func() { p.watch(next) })
	return domain.Acknowledgment{Agent: p.identity, Sequence: snapshot.Sequence, Revision: snapshot.Revision, AppliedAt: now}, nil
}

// Open registers a bounded session against the current exact policy. Empty
// protocol selector lists mean all valid requests on the explicitly approved port.
func (p *PolicyState) Open(ctx context.Context, request Request) (*Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.closed || p.current == nil || len(p.sessions) >= domain.MaxSnapshotRecords {
		return nil, ErrDenied
	}
	if err := p.current.verified.ValidAt(time.Now()); err != nil {
		p.deactivateLocked()
		return nil, ErrDenied
	}
	localSource := request.Source.Scope == p.identity.Scope && request.Source.DestinationID == p.identity.DestinationID
	localTarget := request.Target.Scope == p.identity.Scope && request.Target.DestinationID == p.identity.DestinationID
	if (!localSource && !localTarget) || !permits(p.current.snapshot.Policy, request) {
		return nil, ErrDenied
	}
	sessionCtx, cancel := context.WithCancelCause(ctx)
	session := &Session{ctx: sessionCtx, cancel: cancel, owner: p}
	p.sessions[session] = request
	context.AfterFunc(sessionCtx, func() { p.remove(session) })
	return session, nil
}
func (p *PolicyState) remove(session *Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.sessions, session)
	session.cancel(context.Canceled)
}

// Close cancels every session and joins the policy expiry watcher.
func (p *PolicyState) Close() {
	p.mu.Lock()
	p.closed = true
	p.deactivateLocked()
	p.mu.Unlock()
	p.watchers.Wait()
}
func (p *PolicyState) deactivateLocked() {
	if p.current != nil {
		close(p.current.stop)
		p.current = nil
	}
	for session := range p.sessions {
		delete(p.sessions, session)
		session.cancel(ErrDenied)
	}
}
func (p *PolicyState) watch(installed *installedPolicy) {
	for {
		deadline, changed := installed.verified.watchDeadline()
		timer := time.NewTimer(max(time.Until(deadline), 0))
		select {
		case <-installed.stop:
			timer.Stop()
			return
		case <-changed:
		case <-timer.C:
		}
		timer.Stop()
		p.mu.Lock()
		if p.current != installed {
			p.mu.Unlock()
			return
		}
		if installed.verified.ValidAt(time.Now()) != nil {
			p.deactivateLocked()
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()
	}
}

func validateCompiledPolicy(snapshot domain.Snapshot) error {
	policy := snapshot.Policy
	compiled, err := CompileAt(domain.PolicyInput{Scope: policy.Scope, Generation: policy.Generation, Exports: policy.Exports, Grants: policy.Grants}, snapshot.IssuedAt)
	if err != nil || !policy.ExpiresAt.After(snapshot.IssuedAt) || policy.ExpiresAt.After(compiled.ExpiresAt) {
		return ErrPolicy
	}
	compiled.ExpiresAt = policy.ExpiresAt
	expected, err := json.Marshal(compiled)
	if err != nil {
		return ErrPolicy
	}
	actual, err := json.Marshal(policy)
	if err != nil || !bytes.Equal(expected, actual) {
		return ErrPolicy
	}
	return nil
}
func permits(policy domain.PolicyBundle, request Request) bool {
	if !validRequest(request) {
		return false
	}
	for _, export := range policy.Exports {
		if export.ID != request.ExportID || export.Scope != request.Target.Scope || export.ServiceID != request.Target.ServiceID || !slices.Contains(export.DestinationIDs, request.Target.DestinationID) || export.Port != request.Port || export.Protocol != request.Protocol || export.IdentityProfile != request.IdentityProfile {
			continue
		}
		for _, grant := range policy.Grants {
			if grant.ExportID != export.ID || grant.Source != request.Source {
				continue
			}
			if (len(grant.HTTPMethods) == 0 || slices.Contains(grant.HTTPMethods, request.HTTPMethod)) && (len(grant.HTTPPaths) == 0 || slices.Contains(grant.HTTPPaths, request.HTTPPath)) && (len(grant.GRPCMethods) == 0 || slices.Contains(grant.GRPCMethods, request.GRPCMethod)) {
				return true
			}
		}
	}
	return false
}
func validRequest(request Request) bool {
	switch request.Protocol {
	case domain.HTTP:
		return request.GRPCMethod == "" && domain.ValidHTTPMethod(request.HTTPMethod) && domain.ValidHTTPPath(request.HTTPPath)
	case domain.GRPC:
		return request.HTTPMethod == "" && request.HTTPPath == "" && domain.ValidGRPCMethod(request.GRPCMethod)
	case domain.TCP:
		return request.HTTPMethod == "" && request.HTTPPath == "" && request.GRPCMethod == ""
	default:
		return false
	}
}

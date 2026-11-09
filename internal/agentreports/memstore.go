package agentreports

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/notifications"
)

// MemStore is the in-memory Store, for tests here and in the packages
// that use reports: the same rules as PgStore, under one lock.
type MemStore struct {
	mu      sync.Mutex
	records map[string]*record
	now     func() time.Time
	// notices is where SaveNotice queues notices, as the database
	// transaction would; nil drops them.
	notices notifications.Execer
	// queued records when each delivery was queued, and claims who
	// holds it until when.
	queued map[string]time.Time
	claims map[string]memClaim
}

type memClaim struct {
	owner string
	until time.Time
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{records: map[string]*record{}, now: time.Now, queued: map[string]time.Time{}, claims: map[string]memClaim{}}
}

var _ Store = (*MemStore)(nil)

func (m *MemStore) find(orgID, id string) (*record, error) {
	r, ok := m.records[id]
	if !ok || r.OrgID != orgID {
		return nil, ErrNotFound
	}
	return r, nil
}

// Get implements Store.
func (m *MemStore) Get(_ context.Context, orgID, id string) (*Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.find(orgID, id)
	if err != nil {
		return nil, err
	}
	return r.report(), nil
}

// sorted returns the workspace's records that match keep, by less.
func (m *MemStore) sorted(orgID string, keep func(*record) bool, less func(a, b *record) int) []*Report {
	var rs []*record
	for _, r := range m.records {
		if r.OrgID == orgID && keep(r) {
			rs = append(rs, r)
		}
	}
	slices.SortFunc(rs, less)
	out := make([]*Report, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.report())
	}
	return out
}

func newestFirst(a, b *record) int {
	if c := b.LastSeenAt.Compare(a.LastSeenAt); c != 0 {
		return c
	}
	return strings.Compare(b.ID, a.ID)
}

func oldestFirst(a, b *record) int {
	if c := a.FirstSeenAt.Compare(b.FirstSeenAt); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

// List implements Store.
func (m *MemStore) List(_ context.Context, orgID string, projectIDs []string, f ListFilter) ([]*Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.sorted(orgID, func(r *record) bool {
		return slices.Contains(projectIDs, r.ProjectID) && (f.Status == "all" || r.Status == f.Status) &&
			(f.EnvironmentID == "" || r.EnvironmentID == "" || r.EnvironmentID == f.EnvironmentID)
	}, newestFirst)
	if len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// Open implements Store.
func (m *MemStore) Open(_ context.Context, orgID, projectID, environmentID string) ([]*Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sorted(orgID, func(r *record) bool {
		return r.Status == StatusOpen && (projectID == "" || r.ProjectID == projectID) &&
			(environmentID == "" || r.EnvironmentID == environmentID)
	}, oldestFirst), nil
}

// Investigating implements Store.
func (m *MemStore) Investigating(_ context.Context, orgID string) ([]*Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sorted(orgID, func(r *record) bool {
		return r.Investigation == InvestigationQueued || r.Investigation == InvestigationRunning
	}, oldestFirst), nil
}

// Observe implements Store.
func (m *MemStore) Observe(_ context.Context, sig Signal, reopenAfter, now time.Time) (*Observation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fp := Fingerprint(sig.ProjectID, sig.EnvironmentID, sig.Kind, sig.Component)
	var latest *record
	for _, r := range m.records {
		if r.OrgID != sig.OrgID || r.Fingerprint != fp {
			continue
		}
		if r.Status == StatusOpen {
			change, _ := r.repeat(sig, now)
			return &Observation{Report: r.report(), Change: change}, nil
		}
		if r.ResolvedAt != nil && !r.ResolvedAt.Before(reopenAfter) && (latest == nil || r.ResolvedAt.After(*latest.ResolvedAt)) {
			latest = r
		}
	}
	if latest != nil && latest.counted(sig) {
		return &Observation{Report: latest.report(), Change: ChangeUnchanged}, nil
	}
	if latest != nil {
		latest.reopen(sig, now)
		return &Observation{Report: latest.report(), Change: ChangeReopened}, nil
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	r := newRecord(id, sig, now)
	m.records[id] = r
	return &Observation{Report: r.report(), Change: ChangeOpened}, nil
}

// Clear implements Store.
func (m *MemStore) Clear(_ context.Context, c Clearance) ([]*Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var cleared []*record
	for _, r := range m.records {
		if r.OrgID == c.OrgID && r.ProjectID == c.ProjectID && r.EnvironmentID == c.EnvironmentID && r.Status == StatusOpen &&
			slices.Contains(c.Signals, r.Signal) && (c.Components == nil || slices.Contains(c.Components, r.Component)) && !r.manual() &&
			!r.LastSeenAt.After(c.At) {
			r.Status, r.Resolution = StatusResolved, c.Reason
			r.settle(c.At, "resolved")
			cleared = append(cleared, r)
		}
	}
	slices.SortFunc(cleared, oldestFirst)
	out := make([]*Report, 0, len(cleared))
	for _, r := range cleared {
		out = append(out, r.report())
	}
	return out, nil
}

// Close implements Store.
func (m *MemStore) Close(_ context.Context, orgID, id, by, reason string, at time.Time) (*Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.find(orgID, id)
	if err != nil {
		return nil, err
	}
	if r.Status != StatusOpen {
		return nil, ErrNotOpen
	}
	r.Status, r.ClosedBy, r.Resolution = StatusClosed, by, reason
	r.settle(at, "closed")
	return r.report(), nil
}

// SetInvestigation implements Store.
func (m *MemStore) SetInvestigation(_ context.Context, orgID, id string, from []string, inv Investigation, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.find(orgID, id)
	if errors.Is(err, ErrNotFound) || (err == nil && !slices.Contains(from, r.Investigation)) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	r.investigate(inv, at)
	return true, nil
}

// WriteFindings implements Store.
func (m *MemStore) WriteFindings(_ context.Context, orgID, id string, w Writer, f Findings) (*Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.find(orgID, id)
	if err != nil {
		return nil, err
	}
	queued := r.TaskID == "" && (r.Investigation == InvestigationQueued || r.Investigation == InvestigationRunning) && !r.manual()
	if w.TaskID == "" || (w.TaskID != r.TaskID && w.TaskID != r.OpenedByTask && (!queued || w.Investigates != id)) {
		return nil, ErrNotYourReport
	}
	written := f
	written.Suggestions = slices.Clone(f.Suggestions)
	r.Findings = &written
	r.Revision++
	r.UpdatedAt = f.WrittenAt
	return r.report(), nil
}

// Documents implements Store.
func (m *MemStore) Documents(_ context.Context, orgID string, after time.Time, limit int) ([]*Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.sorted(orgID, func(r *record) bool {
		return (r.Findings != nil || r.Status != StatusOpen) && r.UpdatedAt.After(after)
	}, func(a, b *record) int {
		if c := a.UpdatedAt.Compare(b.UpdatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	if len(out) > limit {
		// Finish the last report's moment, as PgStore does.
		end := limit
		for end < len(out) && out[end].UpdatedAt.Equal(out[limit-1].UpdatedAt) {
			end++
		}
		out = out[:end]
	}
	return out, nil
}

// Pending implements Store.
func (m *MemStore) Pending(_ context.Context, orgID string) ([]*Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sorted(orgID, func(r *record) bool {
		return r.Revision > r.NotifiedRevision || r.Revision > r.IssueRevision ||
			(r.Issue.State == IssueWaiting && r.Status == StatusOpen)
	}, oldestFirst), nil
}

// QueueDelivery implements Store.
func (m *MemStore) QueueDelivery(_ context.Context, orgID, id string, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.find(orgID, id); err != nil {
		return false, nil
	}
	if at, ok := m.queued[id]; ok && !at.Before(now.Add(-DeliveryRequeue)) {
		return false, nil
	}
	m.queued[id] = now
	return true, nil
}

// ClaimDelivery implements Store.
func (m *MemStore) ClaimDelivery(_ context.Context, orgID, id, owner string, lease time.Duration) (*Delivery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.find(orgID, id)
	if err != nil {
		return nil, err
	}
	now := m.now()
	if c, ok := m.claims[id]; ok && c.owner != owner && !c.until.Before(now) {
		return nil, nil
	}
	m.claims[id] = memClaim{owner: owner, until: now.Add(lease)}
	return r.delivery(), nil
}

func (m *MemStore) holds(id, owner string) bool { return m.claims[id].owner == owner }

// RenewDelivery implements Store.
func (m *MemStore) RenewDelivery(_ context.Context, orgID, id, owner string, lease time.Duration) (*Delivery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.find(orgID, id)
	if err != nil {
		return nil, err
	}
	now := m.now()
	c, ok := m.claims[id]
	if !ok || c.owner != owner || !c.until.After(now) {
		return nil, ErrLeaseLost
	}
	m.claims[id] = memClaim{owner: owner, until: now.Add(lease)}
	return r.delivery(), nil
}

// SaveNotice implements Store.
func (m *MemStore) SaveNotice(ctx context.Context, orgID, id, owner string, revision int, pendingNotice string,
	send func(ctx context.Context, q notifications.Execer, seq int) error, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.find(orgID, id)
	if err != nil {
		return false, err
	}
	if r.Revision != revision || r.PendingNotice != pendingNotice || !m.holds(id, owner) {
		return false, nil
	}
	if pendingNotice != "" {
		q := m.notices
		if q == nil {
			q = dropExecer{}
		}
		if err := send(ctx, q, r.NoticeSeq+1); err != nil {
			return false, err
		}
		r.NoticeSeq++
		r.LastNoticeAt, r.PendingNotice = &at, ""
	}
	r.NotifiedRevision = max(r.NotifiedRevision, revision)
	return true, nil
}

// SaveIssue implements Store.
func (m *MemStore) SaveIssue(_ context.Context, orgID, id, owner string, revision int, in IssueSave) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.find(orgID, id)
	if err != nil {
		return err
	}
	if !m.holds(id, owner) {
		return ErrLeaseLost
	}
	r.Issue = Issue{Number: in.Number, URL: in.URL, State: in.State, Error: in.Error}
	r.IssueComments = append(slices.Clone(in.Requeue), r.IssueComments[min(in.Handled, len(r.IssueComments)):]...)
	if in.CommentAt != nil {
		r.IssueCommentAt = cloneTime(in.CommentAt)
	}
	if in.Done {
		r.IssueRevision = max(r.IssueRevision, revision)
	}
	return nil
}

// ReleaseDelivery implements Store.
func (m *MemStore) ReleaseDelivery(_ context.Context, _, id, owner string, done bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.holds(id, owner) {
		return nil
	}
	delete(m.claims, id)
	if done {
		delete(m.queued, id)
	}
	return nil
}

// dropExecer is an Execer that runs nothing.
type dropExecer struct{}

func (dropExecer) Exec(context.Context, string, ...any) (driver.Result, error) { return nil, nil }

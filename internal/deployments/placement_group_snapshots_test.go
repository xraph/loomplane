package deployments

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type snapshotObject struct {
	raw     []byte
	version int64
}
type groupSnapshotFixture struct {
	mu                                                               sync.Mutex
	state                                                            GroupSnapshotState
	objects                                                          map[string]snapshotObject
	captures, writes                                                 int
	deny, loseIssue, loseWrite, losePublish, failPublish, sourceGone bool
	entered, resume                                                  chan struct{}
}

func newGroupSnapshotFixture() *groupSnapshotFixture {
	g := domain.QualifiedGroupPlan{Scope: domain.Scope{OrgID: "org", ProjectID: "project", EnvironmentID: "environment"}, GroupID: "group", Generation: 1, Revision: "review", ConfigRevision: "configuration"}
	return &groupSnapshotFixture{objects: map[string]snapshotObject{}, state: GroupSnapshotState{ID: "claim", Group: g, Selection: environments.RuntimeSelection{OrgID: g.OrgID, ProjectID: g.ProjectID, EnvironmentID: g.EnvironmentID, Revision: "selection"}, Actor: security.Principal{OrgID: g.OrgID, UserID: "actor"}}}
}
func (f *groupSnapshotFixture) BeginSnapshot(_ context.Context, g domain.QualifiedGroupPlan, write bool) (GroupSnapshotState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deny {
		return GroupSnapshotState{}, security.ErrForbidden
	}
	if g.GroupID != f.state.Group.GroupID || g.Scope != f.state.Group.Scope {
		return GroupSnapshotState{}, ErrGroupSnapshot
	}
	if write && f.state.Winner == nil {
		f.state.Epoch++
	}
	s := f.state
	s.Candidates = append([]GroupSnapshotCandidate(nil), s.Candidates...)
	return s, nil
}
func (f *groupSnapshotFixture) AuthorizeSnapshot(_ context.Context, s GroupSnapshotState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deny {
		return security.ErrForbidden
	}
	if s.Epoch != f.state.Epoch || s.ID != f.state.ID {
		return ErrGroupSnapshot
	}
	return nil
}
func (f *groupSnapshotFixture) IssueSnapshot(ctx context.Context, s GroupSnapshotState) (GroupSnapshotCandidate, error) {
	if err := f.AuthorizeSnapshot(ctx, s); err != nil {
		return GroupSnapshotCandidate{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c := GroupSnapshotCandidate{ID: fmt.Sprintf("candidate-%d", len(f.state.Candidates)+1), Epoch: s.Epoch}
	c.Key = GroupSnapshotKey(s.Group, s.ID, c.ID)
	f.state.Candidates = append(f.state.Candidates, c)
	if f.loseIssue {
		f.loseIssue = false
		return GroupSnapshotCandidate{}, errors.New("lost issue commit response")
	}
	return c, nil
}
func (f *groupSnapshotFixture) PublishSnapshot(ctx context.Context, s GroupSnapshotState, c GroupSnapshotCandidate) error {
	if err := f.AuthorizeSnapshot(ctx, s); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPublish {
		f.failPublish = false
		return errors.New("publication rolled back")
	}
	if f.state.Winner != nil && *f.state.Winner != c {
		return ErrGroupSnapshot
	}
	f.state.Winner = &c
	if f.losePublish {
		f.losePublish = false
		return errors.New("lost publication response")
	}
	return nil
}
func (f *groupSnapshotFixture) CaptureRuntimeSelection(context.Context, security.Principal, environments.RuntimeSelection) (map[string]string, map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captures++
	if f.sourceGone {
		return nil, nil, ErrGroupSnapshot
	}
	return map[string]string{"TOKEN": "reviewed"}, map[string]bool{"TOKEN": true}, nil
}
func (f *groupSnapshotFixture) Put(ctx context.Context, key string, raw []byte) error {
	_, err := f.PutVersion(ctx, key, raw)
	return err
}
func (f *groupSnapshotFixture) Get(ctx context.Context, key string) ([]byte, error) {
	return f.GetVersion(ctx, key, 1)
}
func (f *groupSnapshotFixture) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	return nil
}
func (f *groupSnapshotFixture) PutVersion(ctx context.Context, key string, raw []byte) (int64, error) {
	f.mu.Lock()
	entered, resume := f.entered, f.resume
	f.entered = nil
	f.mu.Unlock()
	if entered != nil {
		close(entered)
		select {
		case <-resume:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	o := f.objects[key]
	o.version++
	o.raw = append([]byte(nil), raw...)
	f.objects[key] = o
	f.writes++
	if f.loseWrite {
		f.loseWrite = false
		return 0, errors.New("lost Vault response")
	}
	return o.version, nil
}
func (f *groupSnapshotFixture) GetVersion(_ context.Context, key string, version int64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.objects[key]
	if !ok || o.version != version {
		return nil, ErrNoCredential
	}
	return append([]byte(nil), o.raw...), nil
}
func (f *groupSnapshotFixture) SnapshotMetadata(_ context.Context, key string) (string, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.objects[key]
	if !ok {
		return "", 0, ErrNoCredential
	}
	return "incarnation-" + key, o.version, nil
}
func (f *groupSnapshotFixture) manager() *GroupConfigurationSnapshots {
	return &GroupConfigurationSnapshots{Store: f, Secrets: f, Variables: f}
}

func TestPlacementGroupSnapshotLostResponseNeverRecaptures(t *testing.T) {
	for _, boundary := range []string{"vault-response", "publication-rollback", "publication-response"} {
		t.Run(boundary, func(t *testing.T) {
			f := newGroupSnapshotFixture()
			f.loseWrite = boundary == "vault-response"
			f.failPublish = boundary == "publication-rollback"
			f.losePublish = boundary == "publication-response"
			if _, err := f.manager().Publish(t.Context(), f.state.Group); err == nil {
				t.Fatal("fault not reached")
			}
			f.sourceGone = true
			if _, err := f.manager().Publish(t.Context(), f.state.Group); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				values, flags, err := f.manager().Values(t.Context(), f.state.Group)
				if err != nil || values["TOKEN"] != "reviewed" || !flags["TOKEN"] {
					t.Fatalf("snapshot changed: %v %v", values, err)
				}
			}
			if f.captures != 1 || f.writes != 1 {
				t.Fatalf("recaptured or rewrote: %d/%d", f.captures, f.writes)
			}
			f.objects = map[string]snapshotObject{}
			if _, err := f.manager().Publish(t.Context(), f.state.Group); !errors.Is(err, ErrGroupSnapshot) {
				t.Fatalf("missing winner recaptured: %v", err)
			}
		})
	}
}
func TestPlacementGroupSnapshotLostIssueNeverReusesKey(t *testing.T) {
	f := newGroupSnapshotFixture()
	f.loseIssue = true
	if _, err := f.manager().Publish(t.Context(), f.state.Group); err == nil {
		t.Fatal("fault missing")
	}
	if _, err := f.manager().Publish(t.Context(), f.state.Group); err != nil {
		t.Fatal(err)
	}
	if len(f.state.Candidates) != 2 || f.state.Candidates[0].Key == f.state.Candidates[1].Key || f.writes != 1 {
		t.Fatal("uncertain issued key was reused")
	}
}
func TestPlacementGroupSnapshotLateWriterCannotReplaceWinner(t *testing.T) {
	f := newGroupSnapshotFixture()
	entered, resume := make(chan struct{}), make(chan struct{})
	f.entered, f.resume = entered, resume
	done := make(chan error, 1)
	go func() { _, err := f.manager().Publish(t.Context(), f.state.Group); done <- err }()
	<-entered
	if _, err := f.manager().Publish(t.Context(), f.state.Group); err != nil {
		t.Fatal(err)
	}
	winner := *f.state.Winner
	close(resume)
	if err := <-done; !errors.Is(err, ErrGroupSnapshot) {
		t.Fatalf("stale writer published: %v", err)
	}
	if *f.state.Winner != winner || len(f.objects) != 2 {
		t.Fatal("late write replaced winner or uncertain key was deleted")
	}
	for _, o := range f.objects {
		if o.version != 1 {
			t.Fatal("candidate key written twice")
		}
	}
}
func TestPlacementGroupSnapshotRefusesForeignCandidateAndRevocation(t *testing.T) {
	f := newGroupSnapshotFixture()
	f.state.Candidates = []GroupSnapshotCandidate{{ID: "foreign", Epoch: 1, Key: "another-org/foreign"}}
	if _, err := f.manager().Publish(t.Context(), f.state.Group); !errors.Is(err, ErrGroupSnapshot) {
		t.Fatalf("foreign key accepted: %v", err)
	}
	if f.captures != 0 || f.writes != 0 {
		t.Fatal("foreign candidate touched source")
	}
	f.state.Candidates = nil
	if _, err := f.manager().Publish(t.Context(), f.state.Group); err != nil {
		t.Fatal(err)
	}
	f.deny = true
	if _, _, err := f.manager().Values(t.Context(), f.state.Group); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("revoked reveal: %v", err)
	}
}

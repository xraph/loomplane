package currentauthority

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	authid "github.com/xraph/authsome/id"
	keyid "github.com/xraph/keysmith/id"
	"github.com/xraph/warden/assignment"
	wardenid "github.com/xraph/warden/id"
	"github.com/xraph/warden/store"

	"github.com/xraph/loomplane/internal/security"
)

func ordinaryTestFacts() (binding, ordinaryFacts) {
	b := binding{user: authid.NewUserID().String(), org: authid.NewOrgID().String(), app: authid.NewAppID().String()}
	f := ordinaryFacts{user: b.user, app: b.app, env: authid.NewEnvironmentID().String(), org: b.org, orgApp: b.app, member: authid.NewMemberID().String(), memberRole: security.MemberMember, verified: true}
	return b, f
}

func TestOrdinaryFactsSuppliedInstantAndEligibility(t *testing.T) {
	at := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, keyBacked := range []bool{false, true} {
		for _, required := range []bool{false, true} {
			b, base := ordinaryTestFacts()
			if keyBacked {
				b.key = keyid.NewKeyID().String()
				base.keyID = b.key
				base.keyApp = b.app
				base.keyOrg = b.org
				base.keyCreator = b.user
				base.keyState = "active"
				base.keyAccess = "write"
				base.keyUntil = optionalInstant{at.Add(time.Second), true}
			}
			for _, tt := range []struct {
				name    string
				change  func(*ordinaryFacts)
				allowed bool
			}{
				{"valid", nil, true}, {"unverified", func(f *ordinaryFacts) { f.verified = false }, !required},
				{"deleted", func(f *ordinaryFacts) { f.deleted = true }, false}, {"ban indefinite", func(f *ordinaryFacts) { f.banned = true }, false},
				{"ban future", func(f *ordinaryFacts) { f.banned = true; f.banUntil = optionalInstant{at.Add(time.Second), true} }, false},
				{"ban equality", func(f *ordinaryFacts) { f.banned = true; f.banUntil = optionalInstant{at, true} }, true},
				{"wrong app", func(f *ordinaryFacts) { f.app = authid.NewAppID().String() }, false}, {"wrong workspace app", func(f *ordinaryFacts) { f.orgApp = authid.NewAppID().String() }, false},
				{"absent membership", func(f *ordinaryFacts) { f.member = "" }, false}, {"wrong user", func(f *ordinaryFacts) { f.user = authid.NewUserID().String() }, false},
			} {
				f := base
				if tt.change != nil {
					tt.change(&f)
				}
				err := f.validate(b, required, at)
				if (err == nil) != tt.allowed {
					t.Fatalf("key=%v email=%v %s: %v", keyBacked, required, tt.name, err)
				}
			}
			if keyBacked {
				for _, change := range []func(*ordinaryFacts){func(f *ordinaryFacts) { f.keyRevoked = true }, func(f *ordinaryFacts) { f.keyScopes = true }, func(f *ordinaryFacts) { f.keyUntil = optionalInstant{at, true} }, func(f *ordinaryFacts) { f.keyID = keyid.NewKeyID().String() }} {
					f := base
					change(&f)
					if f.validate(b, required, at) == nil {
						t.Fatal("invalid exact key accepted")
					}
				}
			}
		}
	}
}

type assignmentPages struct {
	store.Store
	rows    []*assignment.Assignment
	offsets []int
	failAt  int
	err     error
}

func (s *assignmentPages) ListAssignments(_ context.Context, f *assignment.ListFilter) ([]*assignment.Assignment, error) {
	s.offsets = append(s.offsets, f.Offset)
	if s.err != nil && f.Offset >= s.failAt {
		return nil, s.err
	}
	if f.Limit != 256 || f.TenantID != "org" || f.SubjectID != "user" || f.SubjectKind != "user" {
		return nil, ErrBinding
	}
	end := min(f.Offset+f.Limit, len(s.rows))
	return s.rows[f.Offset:end], nil
}

func TestAllAssignmentsCompleteStablePages(t *testing.T) {
	for _, count := range []int{1024, 1100} {
		rows := make([]*assignment.Assignment, count)
		for i := range rows {
			rows[i] = &assignment.Assignment{ID: wardenid.NewAssignmentID(), TenantID: "org", SubjectKind: "user", SubjectID: "user", CreatedAt: time.Unix(1, 0)}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID.String() < rows[j].ID.String() })
		s := &assignmentPages{rows: rows}
		_, a := routerAttempt(t, s)
		a.binding = binding{org: "org", user: "user"}
		got, err := a.allAssignments(a.ctx)
		if err != nil || len(got) != count || len(s.offsets) != 5 {
			t.Fatalf("count=%d got=%d calls=%v err=%v", count, len(got), s.offsets, err)
		}
	}
	for _, bad := range []string{"duplicate", "order", "foreign", "backend"} {
		rows := make([]*assignment.Assignment, 1025)
		for i := range rows {
			rows[i] = &assignment.Assignment{ID: wardenid.NewAssignmentID(), TenantID: "org", SubjectKind: "user", SubjectID: "user", CreatedAt: time.Unix(int64(i), 0)}
		}
		s := &assignmentPages{rows: rows}
		switch bad {
		case "duplicate":
			rows[1024] = rows[0]
		case "order":
			rows[1024].CreatedAt = time.Unix(0, 0)
		case "foreign":
			rows[1024].TenantID = "foreign"
		case "backend":
			s.failAt = 1024
			s.err = errors.New("reader failed")
		}
		_, a := routerAttempt(t, s)
		a.binding = binding{org: "org", user: "user"}
		if _, err := a.allAssignments(a.ctx); err == nil {
			t.Fatalf("last-page %s accepted", bad)
		}
	}
}

func TestOrdinaryProofCannotUseGenericDecision(t *testing.T) {
	_, a := routerAttempt(t, &recordingStore{})
	a.profile.mode = installedProfile
	a.profile.ordinary = &ordinaryProfile{}
	called := false
	if _, err := a.Evaluate(a.ctx, func(context.Context) error { called = true; return nil }); !errors.Is(err, ErrDecision) || called {
		t.Fatalf("generic ordinary evaluation err=%v called=%v", err, called)
	}
	b, f := ordinaryTestFacts()
	a.binding = b
	a.profile.ordinary.appID = b.app
	d := &Decision{attempt: a, binding: b, at: time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)}
	if !errors.Is(d.validateOrdinary(a), ErrDecision) {
		t.Fatal("generic proof accepted")
	}
	d.kind = ordinaryDecision
	d.ordinary = &ordinaryProof{permission: security.ProjectRead, facts: f}
	if err := d.validateOrdinary(a); err != nil {
		t.Fatal(err)
	}
	d.ordinary.namespace = "foreign"
	if !errors.Is(d.validateOrdinary(a), ErrDecision) {
		t.Fatal("namespace mutation accepted")
	}
}

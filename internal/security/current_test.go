package security

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/xraph/warden"
	"github.com/xraph/warden/assignment"
	wardenid "github.com/xraph/warden/id"
	"github.com/xraph/warden/store/memory"
)

type currentEngine struct {
	request *warden.CheckRequest
	result  *warden.CheckResult
	err     error
	calls   int
}

func (e *currentEngine) Check(_ context.Context, req *warden.CheckRequest, _ ...warden.CallOption) (*warden.CheckResult, error) {
	e.calls++
	e.request = req
	return e.result, e.err
}
func (*currentEngine) InvalidateSubject(context.Context, string, warden.SubjectKind, string) {
	panic("unexpected invalidation")
}

func TestCheckUnrepairedExactRequestAndNoCallbacks(t *testing.T) {
	p := Principal{UserID: alice, OrgID: org}
	project := "proj_01h455vb4pex5vsknk084sn02q"
	for _, resource := range []string{"", project, "connection-id"} {
		e := &currentEngine{result: &warden.CheckResult{Allowed: true}}
		a := NewAuthorizer(e, nil, func(context.Context, string, string) (string, bool, error) { panic("membership callback") })
		a.teamGrants = func(context.Context, string, string) ([]ScopedRole, error) { panic("team callback") }
		if _, err := a.CheckUnrepaired(t.Context(), p, InfrastructureModify, resource, MemberMember); err != nil {
			t.Fatal(err)
		}
		ns, _ := ProjectNamespace(resource)
		want := &warden.CheckRequest{Subject: warden.Subject{Kind: warden.SubjectUser, ID: alice}, Action: warden.Action{Name: InfrastructureModify.Action}, Resource: warden.Resource{Type: InfrastructureModify.Resource, ID: resource}, TenantID: org, NamespacePath: ns}
		if !reflect.DeepEqual(e.request, want) {
			t.Fatalf("request=%+v want=%+v", e.request, want)
		}
	}
}

func TestCheckUnrepairedFailureAndOwnerRule(t *testing.T) {
	p := Principal{UserID: alice, OrgID: org}
	boom := errors.New("store failed")
	for _, tt := range []struct {
		name   string
		result *warden.CheckResult
		err    error
	}{
		{"nil", nil, nil}, {"deny", &warden.CheckResult{}, nil}, {"backend", &warden.CheckResult{Allowed: true}, boom},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := NewAuthorizer(&currentEngine{result: tt.result, err: tt.err}, nil, nil)
			if _, err := a.CheckUnrepaired(t.Context(), p, ProjectRead, "", MemberOwner); err == nil {
				t.Fatal("accepted failed check")
			}
		})
	}
	e := &currentEngine{result: &warden.CheckResult{Allowed: true}}
	a := NewAuthorizer(e, nil, nil)
	if _, err := a.CheckUnrepaired(t.Context(), p, BackupRetire, "backup", MemberAdmin); !errors.Is(err, ErrForbidden) || e.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, e.calls)
	}
	if _, err := a.CheckUnrepaired(t.Context(), p, BackupRetire, "backup", MemberOwner); err != nil {
		t.Fatal(err)
	}
	for _, q := range []Principal{{UserID: alice, OrgID: org, ReadOnly: true}, {UserID: alice, OrgID: org, Kind: KindAgent}, {UserID: alice, OrgID: org, OnBehalfOf: &p}, {UserID: alice, OrgID: org, Fleet: &FleetScope{}}, {UserID: "loomplane:system", OrgID: org}} {
		if _, err := a.CheckUnrepaired(t.Context(), q, ProjectRead, "", MemberOwner); !errors.Is(err, ErrForbidden) {
			t.Fatal("unsupported actor accepted")
		}
	}
}

func TestCheckUnrepairedCanonicalResourceGrant(t *testing.T) {
	s := memory.New()
	a := authorizerOver(t, s, map[string]string{alice: MemberViewer})
	if err := a.EnsureTenant(t.Context(), org); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetRoleBySlug(t.Context(), org, "", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	grant := &assignment.Assignment{ID: wardenid.NewAssignmentID(), TenantID: org, RoleID: r.ID, SubjectKind: string(warden.SubjectUser), SubjectID: alice, ResourceType: ProjectDelete.Resource, ResourceID: "target"}
	if err = s.CreateAssignment(t.Context(), grant); err != nil {
		t.Fatal(err)
	}
	a.members = func(context.Context, string, string) (string, bool, error) { panic("repair callback") }
	if _, err = a.CheckUnrepaired(t.Context(), Principal{UserID: alice, OrgID: org}, ProjectDelete, "target", MemberViewer); err != nil {
		t.Fatal(err)
	}
	if _, err = a.CheckUnrepaired(t.Context(), Principal{UserID: alice, OrgID: org}, ProjectDelete, "other", MemberViewer); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong resource: %v", err)
	}
	rows, err := s.ListAssignments(t.Context(), &assignment.ListFilter{TenantID: org, SubjectID: alice})
	if err != nil || len(rows) != 1 || rows[0].ID != grant.ID {
		t.Fatalf("assignments changed: %v %v", rows, err)
	}
}

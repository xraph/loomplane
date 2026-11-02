package planning

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/security"
)

type backupGrant struct {
	deny    bool
	calls   int
	project string
	actor   security.Principal
}

func (a *backupGrant) Require(_ context.Context, p security.Principal, permission security.Permission, project string) error {
	if permission != security.BackupManage {
		return nil
	}
	a.calls++
	a.project = project
	a.actor = p
	if a.deny {
		return security.ErrForbidden
	}
	return nil
}

func TestDefaultBackupPermissionIsScopedAndOnlyRequiredForSchedules(t *testing.T) {
	if err := RequireBackupPolicyPermission(t.Context(), nil, security.Principal{}, "project", []Action{{}}); err != nil {
		t.Fatal(err)
	}
	auth := &backupGrant{}
	p := security.Principal{OrgID: "org", UserID: "approver"}
	actions := []Action{{BackupEveryHours: 24}, {BackupEveryHours: 24}}
	if err := RequireBackupPolicyPermission(t.Context(), auth, p, "project", actions); err != nil {
		t.Fatal(err)
	}
	if auth.calls != 1 || auth.project != "project" || auth.actor != p {
		t.Fatalf("authority lost: %+v", auth)
	}
	for _, actor := range []security.Principal{{OrgID: "org"}, {UserID: "u"}, {OrgID: "org", UserID: "u", ReadOnly: true}} {
		if err := RequireBackupPolicyPermission(t.Context(), auth, actor, "project", actions); !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("invalid actor accepted: %+v %v", actor, err)
		}
	}
}

func TestDefaultBackupApprovalChecksTheApproverBeforeSignalling(t *testing.T) {
	store := &unpinnedStore{plan: &Plan{ID: "plan", OrgID: "org", ProjectID: "project", Status: StatusReady, Revision: "revision", Actions: []Action{{BackupEveryHours: 24}}}}
	s := NewService(Deps{Store: store, Authz: &backupGrant{deny: true}})
	p := security.Principal{OrgID: "org", UserID: "approver"}
	if _, err := s.Approve(t.Context(), p, "plan", DecideInput{Revision: "revision"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("approval signalled without backup permission: %v", err)
	}
	if _, err := s.ApplyDecision(t.Context(), "org", "plan", orchestration.Decision{Actor: &p, By: p.UserID, Approve: true, Revision: "revision"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("workflow approved without backup permission: %v", err)
	}
	if store.settled != "" {
		t.Fatalf("denied decision settled as %s", store.settled)
	}
}

func TestDefaultBackupPreviewRequiresCreatorPermission(t *testing.T) {
	for _, denied := range []bool{false, true} {
		auth := &backupGrant{deny: denied}
		s := NewService(Deps{Authz: auth})
		r := &Result{Executable: true, Actions: []Action{{BackupEveryHours: 24}}}
		s.checkBackupPreview(t.Context(), &Plan{OrgID: "org", ProjectID: "project", CreatedBy: "creator"}, r)
		if r.Executable == denied || hasCode(r.Problems, "backup-policy-permission") != denied || auth.actor.UserID != "creator" {
			t.Fatalf("preview permission: %+v %+v", r, auth)
		}
	}
}

func TestDefaultBackupDecisionCannotSubstituteActor(t *testing.T) {
	s := NewService(Deps{Authz: &backupGrant{}})
	plan := &Plan{OrgID: "org", ProjectID: "project", Actions: []Action{{BackupEveryHours: 24}}}
	for _, actor := range []security.Principal{{OrgID: "other", UserID: "approver"}, {OrgID: "org", UserID: "other"}, {OrgID: "org", UserID: "approver", ReadOnly: true}} {
		if err := s.checkBackupDecision(t.Context(), plan, orchestration.Decision{By: "approver", Actor: &actor}); !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("substituted actor accepted: %+v %v", actor, err)
		}
	}
}

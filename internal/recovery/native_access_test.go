package recovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

type nativeAccessTestStore struct {
	NativeCopyWorkStore
	r NativeRestore
}

func (s *nativeAccessTestStore) GetNativeRestore(_ context.Context, org, id string) (*NativeRestore, error) {
	if org != s.r.OrgID || id != s.r.ID {
		return nil, ErrNotFound
	}
	r := copyNativeRestore(s.r)
	return &r, nil
}
func (s *nativeAccessTestStore) WithNativeAccess(ctx context.Context, org, id string, f func(context.Context, NativeRestore) error) error {
	if org != s.r.OrgID || id != s.r.ID {
		return ErrNotFound
	}
	return f(ctx, copyNativeRestore(s.r))
}

type nativeAccessTestAuth struct{ denied security.Permission }

func (a *nativeAccessTestAuth) Require(_ context.Context, p security.Principal, permission security.Permission, project string) error {
	if p.UserID != "operator" || p.OrgID != "org" || project != "project" || permission == a.denied {
		return security.ErrForbidden
	}
	return nil
}

type nativeAccessTestTarget struct {
	NativeAdmissionStore
	revoked bool
}

func (a *nativeAccessTestTarget) CheckNativeTarget(context.Context, *NativeRestore) error {
	if a.revoked {
		return ErrConflict
	}
	return nil
}

type nativeAccessTestBackend struct {
	calls  int
	change func()
	err    error
}

func (b *nativeAccessTestBackend) NativeCredentials(ctx context.Context, _ security.Principal, r NativeRestore, check func(context.Context) error) (*NativeCopyCredentials, error) {
	b.calls++
	if err := check(ctx); err != nil {
		return nil, err
	}
	if b.change != nil {
		b.change()
	}
	if b.err != nil {
		return &NativeCopyCredentials{Password: "must-not-return"}, b.err
	}
	return &NativeCopyCredentials{CopyID: r.ID, Provider: "cloud-sql", ConnectionName: r.ExternalID, Database: "orders", Username: "lp_recovery_reader", Password: "private-reader", ExpiresAt: r.ExpiresAt, CheckedAt: time.Now().UTC()}, nil
}
func TestNativeCredentialAccessRequiresCurrentAuthorityAndNeverReturnsPartialSecrets(t *testing.T) {
	for _, scenario := range []string{"allowed", "read-only", "no-secret-authority", "foreign", "expired", "missing-proof", "cleanup", "target-change", "revoked-during-check", "read-revoked-during-check", "provider-failure"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now().UTC()
			verified := now.Add(-time.Second)
			r := NativeRestore{ID: "copy", OrgID: "org", ProjectID: "project", EnvironmentID: "env", SourceID: "source", PointID: "point", Status: "succeeded", CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), VerifiedAt: &verified, ExternalID: "target:region:copy", PhysicalID: "copy-pin", Source: planning.Binding{OrgID: "org", ProjectID: "project", EnvironmentID: "env", Provider: "cloud-sql", PhysicalID: "source-pin"}, Point: NativePoint{ID: "point", SourceID: "source"}, Target: NativeRestoreTarget{Provider: "cloud-sql", CloudID: "cloud", TargetID: "target", CloudRevision: 1, TargetRevision: 1, GrantRevision: 1}}
			store := &nativeAccessTestStore{r: r}
			auth := &nativeAccessTestAuth{}
			target := &nativeAccessTestTarget{}
			backend := &nativeAccessTestBackend{}
			p := security.Principal{OrgID: "org", UserID: "operator"}
			switch scenario {
			case "read-only":
				auth.denied = security.RestoreManage
			case "no-secret-authority":
				auth.denied = security.SecretManage
			case "foreign":
				p.OrgID = "foreign"
			case "expired":
				store.r.ExpiresAt = now.Add(-time.Second)
			case "missing-proof":
				store.r.VerifiedAt = nil
			case "cleanup":
				store.r.Status = "cleanup_queued"
			case "target-change":
				target.revoked = true
			case "revoked-during-check":
				backend.change = func() { auth.denied = security.SecretManage }
			case "read-revoked-during-check":
				backend.change = func() { auth.denied = security.BackupRead }
			case "provider-failure":
				backend.err = errors.New("private-admin-password in provider diagnostic")
			}
			s := NativeService{Copies: store, Authz: auth, Projects: nativeAccessProjects{}, Admission: target, AccessBackend: backend}
			credentials, err := s.CopyCredentials(t.Context(), p, "copy")
			if scenario == "allowed" {
				if err != nil || credentials == nil || credentials.Password != "private-reader" || backend.calls != 1 {
					t.Fatal("authorized reader access failed", err)
				}
				return
			}
			if err == nil || credentials != nil {
				t.Fatal("unsafe access returned credentials", scenario, err)
			}
			if scenario != "revoked-during-check" && scenario != "read-revoked-during-check" && scenario != "provider-failure" && backend.calls != 0 {
				t.Fatal("denied access reached provider", scenario)
			}
			if scenario == "provider-failure" && err.Error() == backend.err.Error() {
				t.Fatal("private backend diagnostics escaped")
			}
		})
	}
}

type nativeAccessProjects struct{ Projects }

func (nativeAccessProjects) GetProject(_ context.Context, org, id string) (*projects.Project, error) {
	return &projects.Project{ID: id, OrgID: org}, nil
}

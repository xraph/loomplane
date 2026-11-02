package recovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

type nativePointChecker interface {
	CheckPoint(context.Context, security.Principal, string, string) (*NativePoint, error)
}

type nativeSelectedStore struct {
	NativeStore
	source *NativeSource
	point  *NativePoint
	reads  int
}

func (s *nativeSelectedStore) GetNativeSource(context.Context, string, string) (*NativeSource, error) {
	return s.source, nil
}

func (s *nativeSelectedStore) GetNativePoint(_ context.Context, org, source, id string) (*NativePoint, error) {
	s.reads++
	if org != "org" || source != "source" || id != "point" {
		return nil, ErrNotFound
	}
	return s.point, nil
}

type nativeSelectedBackend struct {
	NativeBackend
	point *NativePoint
	err   error
	calls int
}

func (b *nativeSelectedBackend) CheckNativeSnapshot(_ context.Context, _ *planning.Binding, _ NativePoint) (*NativePoint, error) {
	b.calls++
	return b.point, b.err
}

type nativeSelectionAuth struct {
	permission security.Permission
	deny       bool
}

func (a *nativeSelectionAuth) Require(_ context.Context, _ security.Principal, permission security.Permission, project string) error {
	a.permission = permission
	if a.deny || project != "project" {
		return security.ErrForbidden
	}
	return nil
}

func TestNativeSelectedPointRequiresRestorePermissionBeforeReads(t *testing.T) {
	store := &nativeSelectedStore{source: &NativeSource{ID: "source", OrgID: "org", ProjectID: "project"}}
	auth := &nativeSelectionAuth{deny: true}
	s := &NativeService{Store: store, Authz: auth}
	checker, ok := any(s).(nativePointChecker)
	if !ok {
		t.Fatal("native selected point admission check is missing")
	}
	if _, err := checker.CheckPoint(t.Context(), security.Principal{OrgID: "org"}, "source", "point"); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("selected point did not require restore management", err)
	}
	if auth.permission != security.RestoreManage || store.reads != 0 {
		t.Fatal("selected point read before restore authorization")
	}
}

func TestNativeSelectedPointDoesNotTrustCachedAvailability(t *testing.T) {
	for _, scenario := range []string{"original", "provider-error", "changed-reference", "changed-name", "changed-time", "changed-engine", "changed-version", "changed-kind", "changed-key", "changed-encryption", "unavailable", "expired", "nil", "foreign-source", "unsupported-provider", "foreign-point", "wrong-row", "missing-row"} {
		t.Run(scenario, func(t *testing.T) {
			created := time.Date(2026, 10, 21, 2, 0, 0, 1, time.UTC)
			point := NativePoint{ID: "point", SourceID: "source", ProviderID: "native-reference", Name: "daily", Engine: "postgres", Version: "17", Kind: "manual", CreatedAt: created, Encrypted: true, KMSKey: "original-key"}
			current := point
			current.Status, current.Present = "available", true
			store := &nativeSelectedStore{source: &NativeSource{ID: "source", OrgID: "org", ProjectID: "project", Provider: "rds"}, point: &point}
			backend := &nativeSelectedBackend{point: &current}
			switch scenario {
			case "provider-error":
				backend.err = errors.New("private native diagnostic")
			case "changed-reference":
				current.ProviderID = "replacement"
			case "changed-name":
				current.Name = "replacement"
			case "changed-time":
				current.CreatedAt = created.Add(time.Nanosecond)
			case "changed-engine":
				current.Engine = "mysql"
			case "changed-version":
				current.Version = "18"
			case "changed-kind":
				current.Kind = "automated"
			case "changed-key":
				current.KMSKey = "replacement-key"
			case "changed-encryption":
				current.Encrypted = false
			case "unavailable":
				current.Status = "creating"
			case "expired":
				expired := created.Add(time.Hour)
				current.ExpiresAt = &expired
			case "nil":
				backend.point = nil
			case "foreign-source":
				store.source.OrgID = "foreign"
			case "unsupported-provider":
				store.source.Provider = "unsupported"
			case "foreign-point":
				point.SourceID = "foreign-source"
			case "wrong-row":
				point.ID = "another-point"
			case "missing-row":
				store.point = nil
			}
			s := &NativeService{Store: store, Backend: backend, Authz: allowed{}, Projects: fixedProjects{}}
			checker, ok := any(s).(nativePointChecker)
			if !ok {
				t.Fatal("native selected point admission check is missing")
			}
			got, err := checker.CheckPoint(t.Context(), security.Principal{OrgID: "org"}, "source", "point")
			if scenario == "original" {
				if err != nil || got == nil || got.ID != "point" || got.SourceID != "source" || !got.Present || got.LastSeenAt.IsZero() || got.Status != "available" {
					t.Fatalf("fresh point lost its retained identity: %+v %v", got, err)
				}
			} else if err == nil || got != nil {
				t.Fatalf("stale or changed point admitted: %+v %v", got, err)
			}
			if scenario == "foreign-source" || scenario == "unsupported-provider" || scenario == "foreign-point" || scenario == "wrong-row" || scenario == "missing-row" {
				if backend.calls != 0 {
					t.Fatal("foreign retained point reached native provider")
				}
			} else if backend.calls != 1 {
				t.Fatal("selected point did not reread provider exactly once", backend.calls)
			}
		})
	}
}

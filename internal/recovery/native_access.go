package recovery

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

var ErrNativeAccessUnavailable = errors.New("native copy credentials could not be checked; inspect copy status and cloud access before retrying")

// NativeCopyCredentials is an explicit secret response, never an entity or a
// durable recovery record. It excludes administrator and cloud account secrets.
type NativeCopyCredentials struct {
	CopyID                string    `json:"copy_id"`
	Provider              string    `json:"provider"`
	ConnectionName        string    `json:"connection_name"`
	PrivateServiceConnect bool      `json:"private_service_connect,omitempty"`
	Database              string    `json:"database"`
	Username              string    `json:"username"`
	Password              string    `json:"password"`
	ExpiresAt             time.Time `json:"expires_at"`
	CheckedAt             time.Time `json:"checked_at"`
}

type NativeAccessBackend interface {
	NativeCredentials(context.Context, security.Principal, NativeRestore, func(context.Context) error) (*NativeCopyCredentials, error)
}

// CopyCredentials checks current authority again after external inspection.
// The shared store lock keeps cleanup from beginning during that inspection.
func (s *NativeService) CopyCredentials(ctx context.Context, p security.Principal, id string) (*NativeCopyCredentials, error) {
	access, ok := s.Copies.(NativeAccessStore)
	if !ok || s.AccessBackend == nil || s.Admission == nil {
		return nil, ErrInvalid
	}
	r, err := s.GetCopy(ctx, p, id)
	if err != nil {
		return nil, err
	}
	check := func(ctx context.Context, r NativeRestore) error {
		if permissionErr := s.requireNative(ctx, p, r.ProjectID); permissionErr != nil {
			return permissionErr
		}
		if permissionErr := s.Authz.Require(ctx, p, security.BackupRead, r.ProjectID); permissionErr != nil {
			return permissionErr
		}
		if permissionErr := s.Authz.Require(ctx, p, security.SecretManage, r.ProjectID); permissionErr != nil {
			return permissionErr
		}
		if r.OrgID != p.OrgID || r.ID != id || !nativeTargetPinned(r.Target) || !nativeAccessEligible(r, time.Now()) {
			return ErrConflict
		}
		return s.Admission.CheckNativeTarget(ctx, &r)
	}
	if checkErr := check(ctx, *r); checkErr != nil {
		return nil, checkErr
	}
	var out *NativeCopyCredentials
	started := time.Now()
	err = access.WithNativeAccess(ctx, p.OrgID, id, func(ctx context.Context, locked NativeRestore) error {
		guard := func(ctx context.Context) error { return check(ctx, locked) }
		if checkErr := guard(ctx); checkErr != nil {
			return checkErr
		}
		credentials, accessErr := s.AccessBackend.NativeCredentials(ctx, p, copyNativeRestore(locked), guard)
		if accessErr != nil {
			return ErrNativeAccessUnavailable
		}
		if checkErr := guard(ctx); checkErr != nil {
			return checkErr
		}
		if credentials == nil || credentials.CopyID != id || credentials.Provider != locked.Target.Provider || credentials.ConnectionName != locked.ExternalID || credentials.Database == "" || credentials.Username == "" || credentials.Password == "" || !credentials.ExpiresAt.Equal(locked.ExpiresAt) || credentials.CheckedAt.Before(started) || credentials.CheckedAt.After(time.Now()) {
			return ErrNativeAccessUnavailable
		}
		if credentials.PrivateServiceConnect != (locked.Target.Provider == "cloud-sql" && locked.Point.Kind == "pitr") {
			return ErrNativeAccessUnavailable
		}
		out = credentials
		return nil
	})
	if err != nil {
		return nil, err
	}
	if s.Audit != nil {
		s.Audit.Record(ctx, projects.AuditEvent{Action: "service.native_restore.credentials", Severity: "info", Principal: p, ProjectID: r.ProjectID, Attributes: map[string]any{"restore_id": id, "provider": r.Target.Provider}})
	}
	return out, nil
}

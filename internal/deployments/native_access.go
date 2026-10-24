package deployments

import (
	"context"
	"time"

	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

// NativeCredentials authenticates the original reader through Google's
// connector. The caller must supply its own Cloud SQL IAM identity to connect.
func (n *NativeDatabases) NativeCredentials(ctx context.Context, p security.Principal, r recovery.NativeRestore, check func(context.Context) error) (*recovery.NativeCopyCredentials, error) {
	if check == nil || p.OrgID != r.OrgID || p.UserID == "" {
		return nil, recovery.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := check(ctx); err != nil {
		return nil, err
	}
	now := time.Now()
	if r.Status != "succeeded" || r.VerifiedAt == nil || r.VerifiedAt.Before(r.CreatedAt) || r.VerifiedAt.After(now) || !r.ExpiresAt.After(now) || r.CleanupRequestedAt != nil || r.CleanupBy != "" {
		return nil, recovery.ErrConflict
	}
	s, cfg, err := readNativeSQLCopy(r)
	if err != nil {
		return nil, err
	}
	connection := r.Target.NativeProject + ":" + r.Target.Region + ":lp-recovery-" + s.Scope[:32]
	if s.Stage != "ready" || s.Deleted || s.DeleteUncertain || s.Uncertain || s.PhysicalID == "" || s.PhysicalID != r.PhysicalID || r.ExternalID != connection {
		return nil, recovery.ErrConflict
	}
	client, err := n.nativeCopySession(ctx, r)
	if err != nil {
		return nil, err
	}
	guard := s.copyGuard(client, r, cfg, recovery.NativeWorkerControl{Check: check})
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	passwords, err := n.readCopyPasswords(ctx, r, s)
	if err != nil {
		return nil, err
	}
	if checkErr := n.checkNativeSQL(ctx, client, r, s, passwords, guard, true); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	if !r.ExpiresAt.After(time.Now()) {
		return nil, recovery.ErrConflict
	}
	return &recovery.NativeCopyCredentials{CopyID: r.ID, Provider: r.Target.Provider, ConnectionName: connection, Database: databaseName(r.Source.Name), Username: "lp_recovery_" + s.Scope[:32], Password: passwords.ReaderPassword, ExpiresAt: r.ExpiresAt, CheckedAt: time.Now().UTC()}, nil
}

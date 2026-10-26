package deployments

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

// DeleteNativePoint uses the retained source account. It never applies the copy
// cleanup contract to a source backup or treats native availability as SQL proof.
func (n *NativeDatabases) DeleteNativePoint(ctx context.Context, p security.Principal, r recovery.NativeRetirement, c recovery.NativeDeletionControl) (bool, error) {
	if c.Check == nil || c.Before == nil || c.Rejected == nil || p.OrgID == "" || p.OrgID != r.OrgID || p.UserID == "" || p.UserID != r.CreatedBy || r.Source.OrgID != r.OrgID || r.Source.ProjectID != r.ProjectID || r.SourceID == "" || r.PointID != r.Point.ID || r.Point.SourceID != r.SourceID {
		return false, recovery.ErrInvalid
	}
	if err := c.Check(ctx); err != nil {
		return false, err
	}
	cfg, conn, credential, err := n.nativeSourceAccess(ctx, &r.Source)
	if err != nil {
		return false, err
	}
	if cfg.Kind != r.Point.Engine {
		return false, recovery.ErrConflict
	}
	if r.Source.Provider == "rds" {
		if n.AWS == nil {
			return false, recovery.ErrInvalid
		}
		key, keyErr := aws.ParseKey(credential)
		if keyErr != nil {
			return false, keyErr
		}
		selected := aws.DBSnapshot{ID: r.Point.Name, ARN: r.Point.ProviderID, InstanceID: r.Source.ExternalID, ResourceID: r.Source.PhysicalID, Type: r.Point.Kind, Engine: r.Point.Engine, EngineVersion: r.Point.Version, CreatedAt: r.Point.CreatedAt, Encrypted: r.Point.Encrypted, KMSKey: r.Point.KMSKey}
		err = n.AWS.Session(key, r.Source.Region, conn.Account).DeleteRetainedDBSnapshot(ctx, r.Source.PhysicalID, selected, aws.DBRecoveryControl{Check: c.Check, Mutation: aws.DBMutation{Attempted: r.Attempted, Before: c.Before}})
		if aws.DBRecoveryRequestRejected(err) {
			return false, errors.Join(err, c.Rejected())
		}
		if aws.DBRecoveryRequestUncertain(err) {
			return false, errors.Join(recovery.ErrNativePending, err)
		}
	} else {
		if n.Google == nil {
			return false, recovery.ErrInvalid
		}
		key, keyErr := gcp.ParseKey(credential)
		if keyErr != nil {
			return false, keyErr
		}
		if key.ProjectID != conn.Details["project_id"] {
			return false, recovery.ErrConflict
		}
		instance, region, scopeErr := boundSQLInstance(r.Source.ExternalID, key.ProjectID, r.Source.Region)
		if scopeErr != nil {
			return false, scopeErr
		}
		session, sessionErr := n.Google.Session(ctx, key)
		if sessionErr != nil {
			return false, sessionErr
		}
		selected := gcp.SQLBackup{Name: r.Point.ProviderID, Type: r.Point.Kind, DatabaseVersion: "POSTGRES_" + r.Point.Version, CreatedAt: r.Point.CreatedAt, KMSKey: r.Point.KMSKey, ExpiresAt: r.Point.ExpiresAt}
		err = session.DeleteRetainedSQLBackup(ctx, region, instance, r.Source.PhysicalID, selected, r.Attempted, c.Check, c.Before)
		if gcp.RetainedSQLDeleteRejected(err) {
			return false, errors.Join(err, c.Rejected())
		}
		if gcp.RetainedSQLDeleteUncertain(err) {
			return false, errors.Join(recovery.ErrNativePending, err)
		}
	}
	if nativeDeletionReadPending(err) {
		return false, errors.Join(recovery.ErrNativePending, err)
	}
	return err == nil, err
}

func nativeDeletionReadPending(err error) bool {
	if err == nil {
		return false
	}
	var transport net.Error
	var google *gcp.APIError
	var response interface{ HTTPStatusCode() int }
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || aws.DBRecoveryReconciliationPending(err) || errors.As(err, &transport) || errors.As(err, &google) && (google.Status >= http.StatusInternalServerError || google.Status == http.StatusTooManyRequests) || errors.As(err, &response) && (response.HTTPStatusCode() >= http.StatusInternalServerError || response.HTTPStatusCode() == http.StatusTooManyRequests)
}

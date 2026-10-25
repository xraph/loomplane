package deployments

import (
	"context"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recovery"
)

func (s *nativeSQLCopyState) pitrPin(in *gcp.SQLInstance, r recovery.NativeRestore) error {
	pin, err := in.Incarnation(r.Target.NativeProject, r.Target.Region, "lp-recovery-"+s.Scope[:32])
	if err != nil {
		return err
	}
	if s.PhysicalID == "" || pin != s.PhysicalID {
		return recovery.ErrConflict
	}
	return nil
}

func (s *nativeSQLCopyState) clonePITR(ctx context.Context, client *gcp.Session, r recovery.NativeRestore, cfg *planning.ResourceConfig, c recovery.NativeWorkerControl) error {
	if s.Uncertain && s.Operation == "" {
		return errNativeReconcile
	}
	target := s.spec(r, cfg)
	target.Inspect = func(in *gcp.SQLInstance) error {
		pin, err := in.Incarnation(r.Target.NativeProject, r.Target.Region, target.Name)
		if err != nil {
			return err
		}
		if s.PhysicalID != "" && s.PhysicalID != pin {
			return recovery.ErrConflict
		}
		s.PhysicalID = pin
		return s.save(ctx, c)
	}
	source := cloudSQLPITRSourceSpec(cfg, r.Point.Name)
	_, err := client.CloneSQLPointInTime(ctx, source, r.Source.PhysicalID, r.Point.PITR.SettingsDigest, *r.Point.RestoreTime, target, s.track(ctx, c, c.Check, false))
	return err
}

func (s *nativeSQLCopyState) preparePITR(ctx context.Context, client *gcp.Session, r recovery.NativeRestore, cfg *planning.ResourceConfig, c recovery.NativeWorkerControl) error {
	if s.Uncertain && s.Operation == "" {
		return errNativeReconcile
	}
	target := s.spec(r, cfg)
	target.Inspect = func(in *gcp.SQLInstance) error { return s.pitrPin(in, r) }
	_, err := client.PrepareSQLPITRCopy(ctx, target, s.PhysicalID, s.CloneOperation, r.Target.CloudSQLNetwork, s.track(ctx, c, c.Check, false))
	return err
}

func (s *nativeSQLCopyState) pitrCopyGuard(client *gcp.Session, r recovery.NativeRestore, cfg *planning.ResourceConfig, c recovery.NativeWorkerControl) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := c.Check(ctx); err != nil {
			return err
		}
		target := s.spec(r, cfg)
		target.Inspect = func(in *gcp.SQLInstance) error {
			if err := s.pitrPin(in, r); err != nil {
				return err
			}
			ready, err := in.CheckPITRCopyNetwork(target, r.Target.CloudSQLNetwork)
			if err != nil {
				return err
			}
			if !ready {
				return recovery.ErrNativePending
			}
			return nil
		}
		in, err := client.InspectSQLPITRForCleanup(ctx, target, s.CloneOperation, c.Check)
		if err != nil {
			return err
		}
		if in == nil {
			return recovery.ErrConflict
		}
		return c.Check(ctx)
	}
}

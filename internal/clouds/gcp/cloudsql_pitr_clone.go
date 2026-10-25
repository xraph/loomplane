package gcp

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// CloneSQLPointInTime starts standard log recovery only from reviewed inherited
// isolation. This first protocol keeps the project, region and capacity fixed.
// It never patches the source or adopts an existing destination by name.
func (s *Session) CloneSQLPointInTime(ctx context.Context, source SQLInstanceSpec, physical, digest string, at time.Time, target SQLInstanceSpec, track SQLMutation) (*SQLInstance, error) {
	_, offset := at.Zone()
	raw, decodeErr := hex.DecodeString(digest)
	if decodeErr != nil || len(raw) != 32 || at.IsZero() || offset != 0 || !sqlNativeName(source.Name) || !sqlNativeName(target.Name) || len(source.Name) > 63 || len(target.Name) > 63 || target.Name == source.Name || source.Region == "" || source.Region != target.Region || source.Tier != target.Tier || source.DatabaseVersion != "POSTGRES_17" || source.DatabaseVersion != target.DatabaseVersion || source.Storage == nil || target.Storage == nil || *source.Storage != *target.Storage || target.Existing || target.Intent == "" || target.Inspect == nil || track.Check == nil {
		return nil, ErrSQLPITRUnavailable
	}
	if err := s.checkSQLBackupSource(source.Region, source.Name, physical); err != nil {
		return nil, err
	}
	if err := track.check(ctx); err != nil {
		return nil, err
	}
	if track.Resume == "" {
		if _, err := s.GetSQLInstance(ctx, target.Name); !IsNotFound(err) {
			if err != nil {
				return nil, err
			}
			return nil, errors.New("cloud sql PITR destination already exists without a saved clone operation")
		}
	}
	operation := track.Resume
	started := track.Started
	track.Started = func(name string) error {
		if started == nil {
			return errors.New("cloud sql PITR requires durable operation capture")
		}
		if err := started(name); err != nil {
			return err
		}
		operation = name
		return nil
	}
	track.strictIdentity = true
	if err := s.mutateSQL(ctx, target.Name, "Restoring the selected time into an isolated copy", track, func(op *sqlOperation) (bool, error) {
		// This runs after the uncertain request has been durably saved. Validate
		// current source settings and rolling bounds immediately before POST.
		window, err := s.ReadSQLPITRWindow(ctx, source.Region, source.Name, physical, source)
		if err != nil {
			return false, err
		}
		if window.SettingsDigest != digest || at.Before(window.Earliest) || !at.Before(window.Latest) {
			return false, ErrSQLPITRUnavailable
		}
		if err := track.check(ctx); err != nil {
			return false, err
		}
		body := map[string]any{"cloneContext": map[string]string{"kind": "sql#cloneContext", "destinationInstanceName": target.Name, "pointInTime": at.UTC().Format(time.RFC3339Nano)}}
		return false, s.do(ctx, http.MethodPost, s.sqlPath("/instances/"+url.PathEscape(source.Name)+"/clone"), body, op)
	}, "CLONE"); err != nil {
		return nil, err
	}
	in, err := s.InspectSQLPITRForCleanup(ctx, target, operation, track.Check)
	if err != nil {
		return nil, err
	}
	if in == nil {
		return nil, errors.New("cloud sql clone completed without its original instance")
	}
	if _, err := in.PITRIsolation(target); err != nil {
		return nil, err
	}
	return in, nil
}

// InspectSQLPITRForCleanup captures a partial clone using the saved operation
// and its target creation timing. Inherited source labels are not ownership.
// A failed terminal clone can still leave a copy that must be disposed of.
func (s *Session) InspectSQLPITRForCleanup(ctx context.Context, target SQLInstanceSpec, operation string, check func(context.Context) error) (*SQLInstance, error) {
	if !validSQLPathID(operation) || !sqlNativeName(target.Name) || target.Region == "" || target.DatabaseVersion != "POSTGRES_17" || target.Inspect == nil || check == nil {
		return nil, errors.New("cloud sql PITR cleanup requires the saved clone operation and target")
	}
	var op sqlOperation
	for {
		if err := check(ctx); err != nil {
			return nil, err
		}
		if err := s.do(ctx, http.MethodGet, s.sqlPath("/operations/"+url.PathEscape(operation)), nil, &op); err != nil {
			return nil, err
		}
		if err := s.validateSQLOperation(op, operation, target.Name, []string{"CLONE"}, true); err != nil {
			return nil, err
		}
		if op.Warning != nil {
			return nil, errors.New("cloud sql clone operation has incomplete metadata")
		}
		if op.Status == "DONE" {
			break
		}
		if err := s.wait(ctx); err != nil {
			return nil, err
		}
	}
	if err := check(ctx); err != nil {
		return nil, err
	}
	in, err := s.GetSQLInstance(ctx, target.Name)
	if IsNotFound(err) {
		return nil, check(ctx)
	}
	if err != nil {
		return nil, err
	}
	if _, pinErr := in.Incarnation(s.key.ProjectID, target.Region, target.Name); pinErr != nil {
		return nil, pinErr
	}
	created, err := time.Parse(time.RFC3339Nano, in.CreateTime)
	if err != nil || op.InsertedAt.IsZero() || op.EndedAt.IsZero() || op.EndedAt.Before(op.InsertedAt) || created.Before(op.InsertedAt) || created.After(op.EndedAt) || in.Kind != "sql#instance" || in.DatabaseVersion != target.DatabaseVersion {
		return nil, errors.New("cloud sql clone instance does not match its captured terminal operation")
	}
	if err := target.Inspect(in); err != nil {
		return nil, err
	}
	return in, check(ctx)
}

// DeleteSQLPITRCopy also handles copies that failed before receiving recovery
// labels. Its CLONE operation and original creation pin remain mandatory.
func (s *Session) DeleteSQLPITRCopy(ctx context.Context, name, region, physical, cloneOperation string, track SQLMutation) error {
	if err := s.checkSQLBackupSource(region, name, physical); err != nil {
		return err
	}
	if !validSQLPathID(cloneOperation) || track.Check == nil {
		return errors.New("cloud sql PITR disposal requires the original clone operation and worker guard")
	}
	guard := track.Check
	witnessed := false
	checkPin := func(in *SQLInstance) error {
		pin, err := in.Incarnation(s.key.ProjectID, region, name)
		if err != nil {
			return err
		}
		if pin != physical || in.Kind != "sql#instance" || in.DatabaseVersion != "POSTGRES_17" {
			return errors.New("cloud sql PITR disposal target does not match the original creation")
		}
		return nil
	}
	return s.deleteSQLRecoveryCopy(ctx, name, track, func(ctx context.Context, in *SQLInstance) error {
		if err := checkPin(in); err != nil {
			return err
		}
		if !witnessed {
			spec := SQLInstanceSpec{Name: name, Region: region, DatabaseVersion: "POSTGRES_17", Inspect: checkPin}
			original, err := s.InspectSQLPITRForCleanup(ctx, spec, cloneOperation, guard)
			if err != nil {
				return err
			}
			if original == nil {
				return errors.New("cloud sql PITR copy disappeared while checking its clone witness")
			}
			witnessed = true
		}
		return nil
	})
}

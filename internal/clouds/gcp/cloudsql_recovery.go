package gcp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// RestoreSQLBackup creates a new PostgreSQL copy in a separate recovery project.
// A captured operation can resume; an existing unowned instance is never reused.
func (s *Session) RestoreSQLBackup(ctx context.Context, backup string, spec SQLInstanceSpec, track SQLMutation) (*SQLInstance, error) {
	parts := strings.Split(backup, "/")
	if len(parts) != 4 || parts[0] != "projects" || parts[2] != "backups" || !validSQLPathID(parts[1]) || !validSQLPathID(parts[3]) || parts[1] == s.key.ProjectID {
		return nil, errors.New("cloud sql recovery requires a canonical backup in a different source project")
	}
	if s.key.ProjectID == "" || !sqlNativeName(spec.Name) || len(spec.Name) > 63 || spec.DatabaseVersion != "POSTGRES_17" || spec.Region == "" || spec.Tier == "" || spec.Intent == "" || len(spec.Intent) > 63 || spec.Existing || spec.Inspect == nil || track.Check == nil {
		return nil, errors.New("cloud sql recovery requires a new reviewed PostgreSQL target and durable ownership guards")
	}
	if spec.Storage != nil && (spec.Storage.SizeGB < 10 || spec.Storage.Type != "PD_SSD") {
		return nil, errors.New("cloud sql recovery requires reviewed SSD storage of at least 10 GiB")
	}
	if err := track.check(ctx); err != nil {
		return nil, err
	}
	if track.Resume == "" {
		if _, err := s.GetSQLInstance(ctx, spec.Name); !IsNotFound(err) {
			if err != nil {
				return nil, err
			}
			return nil, errors.New("cloud sql recovery target already exists without a captured restore operation")
		}
	}
	track.strictIdentity = true
	if err := s.mutateSQL(ctx, spec.Name, "Restoring the native backup into a new copy", track, func(op *sqlOperation) (bool, error) {
		settings := map[string]any{
			"tier": spec.Tier, "edition": "ENTERPRISE", "availabilityType": "ZONAL",
			"connectorEnforcement": "REQUIRED", "deletionProtectionEnabled": false,
			"ipConfiguration":     map[string]any{"ipv4Enabled": true, "authorizedNetworks": []any{}, "privateNetwork": "", "pscConfig": map[string]bool{"pscEnabled": false}},
			"userLabels":          map[string]string{"managed-by": "loomplane", SQLIntentLabel: spec.Intent},
			"backupConfiguration": map[string]bool{"enabled": false, "pointInTimeRecoveryEnabled": false},
		}
		if disk := spec.Storage; disk != nil {
			settings["dataDiskSizeGb"], settings["dataDiskType"], settings["storageAutoResize"] = strconv.FormatInt(int64(disk.SizeGB), 10), disk.Type, false
		}
		body := map[string]any{
			"backup":                  backup,
			"restoreInstanceSettings": map[string]any{"name": spec.Name, "project": s.key.ProjectID, "region": spec.Region, "databaseVersion": spec.DatabaseVersion, "settings": settings},
			"restoreInstanceClearOverridesFieldNames": []string{"settings.ipConfiguration.privateNetwork", "settings.ipConfiguration.authorizedNetworks", "settings.ipConfiguration.pscConfig", "settings.locationPreference"},
		}
		return false, s.do(ctx, http.MethodPost, s.sqlPath("/instances/"+url.PathEscape(spec.Name)+"/restoreBackup"), body, op)
	}, "RESTORE_VOLUME"); err != nil {
		return nil, err
	}
	for {
		if err := track.check(ctx); err != nil {
			return nil, err
		}
		in, err := s.GetSQLInstance(ctx, spec.Name)
		if err != nil {
			return nil, err
		}
		if _, err := in.Incarnation(s.key.ProjectID, spec.Region, spec.Name); err != nil {
			return nil, err
		}
		if in.Kind != "sql#instance" || in.DatabaseVersion != spec.DatabaseVersion || in.Settings.UserLabels[SQLIntentLabel] != spec.Intent || in.Settings.UserLabels["managed-by"] != "loomplane" {
			return nil, errors.New("cloud sql recovery target does not match its saved creation intent")
		}
		// Save the creation pin before any further action, including cleanup of
		// a copy whose effective network settings fail the isolation check.
		if err := spec.Inspect(in); err != nil {
			return nil, err
		}
		if settingsErr := in.CheckRecoverySettings(spec); settingsErr != nil {
			return nil, settingsErr
		}
		switch in.State {
		case "RUNNABLE":
			return in, nil
		case "PENDING_CREATE", "MAINTENANCE":
			if err := s.wait(ctx); err != nil {
				return nil, err
			}
		default:
			return nil, errors.New("cloud sql recovery copy is not in a recoverable readiness state")
		}
	}
}

// CheckRecoverySettings is shared by creation and later guarded copy actions.
// A preserved creation pin does not imply that the network stayed isolated.
func (in *SQLInstance) CheckRecoverySettings(spec SQLInstanceSpec) error {
	if in == nil {
		return errors.New("cloud sql recovery target metadata is missing")
	}
	network := in.Settings.IPConfiguration
	if in.Settings.Tier != spec.Tier || in.Settings.ConnectorEnforcement != "REQUIRED" || network == nil || network.IPv4Enabled == nil || !*network.IPv4Enabled || len(network.AuthorizedNetworks) != 0 || network.PrivateNetwork != "" || network.PSC != nil && network.PSC.Enabled {
		return errors.New("cloud sql recovery target does not have its reviewed connector-only network settings")
	}
	if disk := spec.Storage; disk != nil && (in.Settings.DataDiskSizeGB != int64(disk.SizeGB) || in.Settings.DataDiskType != disk.Type || in.Settings.StorageAutoResize == nil || *in.Settings.StorageAutoResize) {
		return errors.New("cloud sql recovery target does not match its reviewed storage bounds")
	}
	return nil
}

// InspectSQLRecoveryForCleanup observes a captured restore without initiating
// another one. A failed terminal operation can leave no instance or a partial
// owned copy. A surviving copy needs creation timing before it can be pinned.
func (s *Session) InspectSQLRecoveryForCleanup(ctx context.Context, spec SQLInstanceSpec, operation string, check func(context.Context) error) (*SQLInstance, error) {
	if !validSQLPathID(operation) || !sqlNativeName(spec.Name) || spec.Region == "" || spec.DatabaseVersion != "POSTGRES_17" || spec.Intent == "" || spec.Inspect == nil || check == nil {
		return nil, errors.New("cloud sql cleanup requires the captured restore and its original intent")
	}
	var op sqlOperation
	for {
		if err := check(ctx); err != nil {
			return nil, err
		}
		if err := s.do(ctx, http.MethodGet, s.sqlPath("/operations/"+url.PathEscape(operation)), nil, &op); err != nil {
			return nil, err
		}
		if err := s.validateSQLOperation(op, operation, spec.Name, []string{"RESTORE_VOLUME"}, true); err != nil {
			return nil, err
		}
		if op.Warning != nil {
			return nil, errors.New("cloud sql restore operation has incomplete metadata")
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
	in, err := s.GetSQLInstance(ctx, spec.Name)
	if IsNotFound(err) {
		return nil, check(ctx)
	}
	if err != nil {
		return nil, err
	}
	if _, pinErr := in.Incarnation(s.key.ProjectID, spec.Region, spec.Name); pinErr != nil {
		return nil, pinErr
	}
	created, err := time.Parse(time.RFC3339Nano, in.CreateTime)
	if err != nil || op.InsertedAt.IsZero() || op.EndedAt.IsZero() || op.EndedAt.Before(op.InsertedAt) || created.Before(op.InsertedAt) || created.After(op.EndedAt) || in.Kind != "sql#instance" || in.DatabaseVersion != spec.DatabaseVersion || in.Settings.UserLabels[SQLIntentLabel] != spec.Intent || in.Settings.UserLabels["managed-by"] != "loomplane" {
		return nil, errors.New("cloud sql partial recovery copy does not match its terminal restore operation")
	}
	if inspectErr := spec.Inspect(in); inspectErr != nil {
		return nil, inspectErr
	}
	return in, check(ctx)
}

// DeleteSQLRecovery disposes only the pinned temporary copy. It does not request
// a final backup and does not share the protected live-instance removal path.
func (s *Session) DeleteSQLRecovery(ctx context.Context, name, region, physical, intent string, track SQLMutation) error {
	if err := s.checkSQLBackupSource(region, name, physical); err != nil {
		return err
	}
	if intent == "" || track.Check == nil {
		return errors.New("cloud sql copy cleanup requires its saved intent and active worker guard")
	}
	guard := track.Check
	if err := guard(ctx); err != nil {
		return err
	}
	requested := track.Resume != ""
	checkCopy := func(ctx context.Context) error {
		if err := guard(ctx); err != nil {
			return err
		}
		in, err := s.GetSQLInstance(ctx, name)
		if IsNotFound(err) && requested {
			return nil
		}
		if err != nil {
			return err
		}
		pin, err := in.Incarnation(s.key.ProjectID, region, name)
		if err != nil {
			return err
		}
		if pin != physical || in.Kind != "sql#instance" || in.Settings.UserLabels[SQLIntentLabel] != intent || in.Settings.UserLabels["managed-by"] != "loomplane" {
			return errors.New("cloud sql cleanup target does not match the original temporary copy")
		}
		return nil
	}
	if _, err := s.GetSQLInstance(ctx, name); IsNotFound(err) && !requested {
		return nil
	} else if err != nil && !IsNotFound(err) {
		return err
	}
	track.Check, track.strictIdentity = checkCopy, true
	if err := s.mutateSQL(ctx, name, "Removing the temporary recovery copy", track, func(op *sqlOperation) (bool, error) {
		err := s.do(ctx, http.MethodDelete, s.sqlPath("/instances/"+url.PathEscape(name))+"?enableFinalBackup=false", nil, op)
		if err == nil {
			requested = true
		}
		return false, err
	}, "DELETE"); err != nil {
		return err
	}
	for {
		if err := guard(ctx); err != nil {
			return err
		}
		if _, err := s.GetSQLInstance(ctx, name); IsNotFound(err) {
			return nil
		} else if err != nil {
			return err
		}
		if err := checkCopy(ctx); err != nil {
			return err
		}
		if err := s.wait(ctx); err != nil {
			return err
		}
	}
}

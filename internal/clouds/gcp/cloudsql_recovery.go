package gcp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
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
		network := in.Settings.IPConfiguration
		if in.Settings.Tier != spec.Tier || in.Settings.ConnectorEnforcement != "REQUIRED" || network == nil || network.IPv4Enabled == nil || !*network.IPv4Enabled || len(network.AuthorizedNetworks) != 0 || network.PrivateNetwork != "" || network.PSC != nil && network.PSC.Enabled {
			return nil, errors.New("cloud sql recovery target does not have its reviewed connector-only network settings")
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

package gcp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/manageddb"
)

// SQLInstance is the part of a Cloud SQL instance Loomplane reads.
type SQLInstance struct {
	Name            string `json:"name"`
	ConnectionName  string `json:"connectionName"`
	DatabaseVersion string `json:"databaseVersion"`
	State           string `json:"state"`
}

// SQLInstanceSpec is what Loomplane creates an instance from.
type SQLInstanceSpec struct {
	Name            string
	DatabaseVersion string
	Region          string
	Tier            string
	// Storage is an explicit reviewed request. Nil preserves legacy defaults.
	Storage *SQLStorageSettings
}

// SQLStorageSettings are the native disk and recovery settings requested at creation.
type SQLStorageSettings struct {
	SizeGB          int32
	Type            string
	RetainedBackups int32
	RecoveryDays    int32
}

func (s *Session) sqlPath(path string) string {
	return s.c.url("sqladmin.googleapis.com", "/v1/projects/"+s.key.ProjectID+path)
}

type sqlOperation struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  *struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	} `json:"error"`
}

// waitSQL polls a Cloud SQL operation until it's done. Creating an
// instance takes minutes.
func (s *Session) waitSQL(ctx context.Context, op sqlOperation, message string) error {
	for op.Status != "DONE" {
		progress(ctx, message)
		if err := s.wait(ctx); err != nil {
			return err
		}
		if err := s.do(ctx, http.MethodGet, s.sqlPath("/operations/"+url.PathEscape(op.Name)), nil, &op); err != nil {
			return err
		}
	}
	if op.Error != nil && len(op.Error.Errors) > 0 {
		msgs := make([]string, 0, len(op.Error.Errors))
		for _, e := range op.Error.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("cloud sql: %s", strings.Join(msgs, "; "))
	}
	return nil
}

// GetSQLInstance reads an instance; a missing one is IsNotFound.
func (s *Session) GetSQLInstance(ctx context.Context, name string) (*SQLInstance, error) {
	var in SQLInstance
	if err := s.do(ctx, http.MethodGet, s.sqlPath("/instances/"+url.PathEscape(name)), nil, &in); err != nil {
		return nil, err
	}
	return &in, nil
}

// EnsureSQLInstance creates an instance if there isn't one by that name,
// and waits until it serves. resume is an operation an earlier attempt
// started, to wait on instead of starting another; started reports the
// operation this one starts, so a retry can resume it.
func (s *Session) EnsureSQLInstance(ctx context.Context, spec SQLInstanceSpec, resume string, started func(op string)) (*SQLInstance, error) {
	if resume != "" {
		if err := s.waitSQL(ctx, sqlOperation{Name: resume}, "Creating the database instance"); err != nil {
			return nil, err
		}
	}
	in, err := s.GetSQLInstance(ctx, spec.Name)
	if IsNotFound(err) {
		var op sqlOperation
		settings := map[string]any{
			"tier": spec.Tier, "edition": "ENTERPRISE",
			"ipConfiguration":     map[string]any{"ipv4Enabled": true},
			"backupConfiguration": map[string]any{"enabled": true},
			"userLabels":          map[string]string{"managed-by": "loomplane"},
		}
		if disk := spec.Storage; disk != nil {
			settings["dataDiskSizeGb"], settings["dataDiskType"] = strconv.FormatInt(int64(disk.SizeGB), 10), disk.Type
			settings["storageAutoResize"], settings["availabilityType"] = false, "ZONAL"
			settings["backupConfiguration"] = map[string]any{
				"enabled": true, "pointInTimeRecoveryEnabled": true, "transactionLogRetentionDays": disk.RecoveryDays,
				"backupRetentionSettings": map[string]any{"retentionUnit": "COUNT", "retainedBackups": disk.RetainedBackups},
			}
		}
		err = s.do(ctx, http.MethodPost, s.sqlPath("/instances"), map[string]any{
			"name": spec.Name, "databaseVersion": spec.DatabaseVersion, "region": spec.Region,
			"settings": settings,
		}, &op)
		if err != nil {
			return nil, err
		}
		started(op.Name)
		if werr := s.waitSQL(ctx, op, "Creating the database instance"); werr != nil {
			return nil, werr
		}
		in, err = s.GetSQLInstance(ctx, spec.Name)
	}
	if err != nil {
		return nil, err
	}
	for in.State != "RUNNABLE" {
		progress(ctx, "Waiting for the database instance ("+strings.ToLower(in.State)+")")
		if werr := s.wait(ctx); werr != nil {
			return nil, werr
		}
		if in, err = s.GetSQLInstance(ctx, spec.Name); err != nil {
			return nil, err
		}
	}
	return in, nil
}

// EnsureDatabase creates a database on an instance, if it isn't there.
func (s *Session) EnsureDatabase(ctx context.Context, instance, name string) error {
	var op sqlOperation
	err := s.do(ctx, http.MethodPost, s.sqlPath("/instances/"+url.PathEscape(instance)+"/databases"), map[string]string{"name": name}, &op)
	if IsConflict(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.waitSQL(ctx, op, "Creating the database")
}

// SetUser creates a user with password, or sets an existing one's.
func (s *Session) SetUser(ctx context.Context, instance, name, password string) error {
	var op sqlOperation
	body := map[string]string{"name": name, "password": password}
	base := "/instances/" + url.PathEscape(instance) + "/users"
	err := s.do(ctx, http.MethodPost, s.sqlPath(base), body, &op)
	if IsConflict(err) {
		err = s.do(ctx, http.MethodPut, s.sqlPath(base+"?name="+url.QueryEscape(name)), body, &op)
	}
	if err != nil {
		return err
	}
	return s.waitSQL(ctx, op, "Setting the database user")
}

// DeleteSQLInstance removes the instance with a final backup retained for
// 30 days. Backup refusal blocks removal; there is no unprotected fallback.
// One that's already gone is not an error.
func (s *Session) DeleteSQLInstance(ctx context.Context, name string) error {
	var op sqlOperation
	query := url.Values{"enableFinalBackup": {"true"}, "finalBackupTtlDays": {strconv.Itoa(manageddb.CloudSQLFinalBackupDays)}}
	err := s.do(ctx, http.MethodDelete, s.sqlPath("/instances/"+url.PathEscape(name))+"?"+query.Encode(), nil, &op)
	if IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.waitSQL(ctx, op, "Deleting the database instance")
}

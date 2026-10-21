package gcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
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
	// Existing permits a known binding to reconcile its original instance.
	Existing bool
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

// SQLMutation connects a native mutation to its durable deployment checkpoint.
// Before persists an uncertain intent before the request. Started must persist
// the returned operation before polling or performing another mutation.
type SQLMutation struct {
	Resume  string
	Before  func() error
	Started func(string) error
}

type sqlOperation struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	TargetID      string `json:"targetId"`
	TargetProject string `json:"targetProject"`
	Type          string `json:"operationType"`
	Error         *struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	} `json:"error"`
}

func (s *Session) validateSQLOperation(op sqlOperation, name, instance string, types []string) error {
	if op.Name == "" || strings.ContainsAny(op.Name, "/\\?#") || (name != "" && name != op.Name) ||
		!slices.Contains([]string{"PENDING", "RUNNING", "DONE"}, op.Status) ||
		(op.TargetProject != "" && op.TargetProject != s.key.ProjectID) ||
		(op.TargetID != "" && op.TargetID != instance) ||
		(op.Type != "" && !slices.Contains(types, op.Type)) {
		return errors.New("cloud sql operation does not match the requested instance and mutation")
	}
	return nil
}

// mutateSQL resumes the captured operation, or records the intent and operation
// around a new request. Missing operations never authorize another mutation.
func (s *Session) mutateSQL(ctx context.Context, instance, message string, track SQLMutation, begin func(*sqlOperation) (bool, error), types ...string) error {
	var op sqlOperation
	if track.Resume != "" {
		if strings.ContainsAny(track.Resume, "/\\?#") {
			return errors.New("invalid cloud sql operation identity")
		}
		if err := s.do(ctx, http.MethodGet, s.sqlPath("/operations/"+url.PathEscape(track.Resume)), nil, &op); err != nil {
			return fmt.Errorf("read saved cloud sql operation: %w", err)
		}
	} else {
		if track.Before == nil || track.Started == nil {
			return errors.New("cloud sql mutation requires a durable checkpoint")
		}
		if err := track.Before(); err != nil {
			return err
		}
		done, err := begin(&op)
		if err != nil {
			return err
		}
		// Only recognized HTTP absence/conflict completes without an operation.
		if done {
			return nil
		}
	}
	if err := s.validateSQLOperation(op, track.Resume, instance, types); err != nil {
		return err
	}
	if track.Resume == "" {
		if err := track.Started(op.Name); err != nil {
			return err
		}
	}
	for op.Status != "DONE" {
		progress(ctx, message)
		if err := s.wait(ctx); err != nil {
			return err
		}
		var next sqlOperation
		if err := s.do(ctx, http.MethodGet, s.sqlPath("/operations/"+url.PathEscape(op.Name)), nil, &next); err != nil {
			return err
		}
		if err := s.validateSQLOperation(next, op.Name, instance, types); err != nil {
			return err
		}
		op = next
	}
	if op.Error != nil {
		if len(op.Error.Errors) == 0 {
			return errors.New("cloud sql operation failed without error details")
		}
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
// and waits until it serves. track saves the request intent and returned operation so a retry can resume it.
func (s *Session) EnsureSQLInstance(ctx context.Context, spec SQLInstanceSpec, track SQLMutation) (*SQLInstance, error) {
	if track.Resume != "" {
		if err := s.mutateSQL(ctx, spec.Name, "Creating the database instance", track, nil, "CREATE"); err != nil {
			return nil, err
		}
	}
	in, err := s.GetSQLInstance(ctx, spec.Name)
	if err == nil && track.Resume == "" && !spec.Existing {
		return nil, errors.New("cloud sql instance already exists without a saved operation or binding; review its ownership before adoption")
	}
	if IsNotFound(err) && track.Resume == "" {
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
		err = s.mutateSQL(ctx, spec.Name, "Creating the database instance", track, func(op *sqlOperation) (bool, error) {
			createErr := s.do(ctx, http.MethodPost, s.sqlPath("/instances"), map[string]any{
				"name": spec.Name, "databaseVersion": spec.DatabaseVersion, "region": spec.Region, "settings": settings,
			}, op)
			return false, createErr
		}, "CREATE")
		if err != nil {
			return nil, err
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
func (s *Session) EnsureDatabase(ctx context.Context, instance, name string, track SQLMutation) error {
	return s.mutateSQL(ctx, instance, "Creating the database", track, func(op *sqlOperation) (bool, error) {
		err := s.do(ctx, http.MethodPost, s.sqlPath("/instances/"+url.PathEscape(instance)+"/databases"), map[string]string{"name": name}, op)
		if IsConflict(err) {
			return true, nil
		}
		return false, err
	}, "CREATE_DATABASE")
}

// SetUser creates a user with password, or sets an existing one's.
func (s *Session) SetUser(ctx context.Context, instance, name, password string, track SQLMutation) error {
	return s.mutateSQL(ctx, instance, "Setting the database user", track, func(op *sqlOperation) (bool, error) {
		body := map[string]string{"name": name, "password": password}
		base := "/instances/" + url.PathEscape(instance) + "/users"
		err := s.do(ctx, http.MethodPost, s.sqlPath(base), body, op)
		if IsConflict(err) {
			err = s.do(ctx, http.MethodPut, s.sqlPath(base+"?name="+url.QueryEscape(name)), body, op)
		}
		return false, err
	}, "CREATE_USER", "UPDATE_USER")
}

// DeleteSQLInstance removes the instance with a final backup retained for
// 30 days. A retry waits for its saved operation without another deletion.
func (s *Session) DeleteSQLInstance(ctx context.Context, name string, track SQLMutation) error {
	return s.mutateSQL(ctx, name, "Deleting the database instance", track, func(op *sqlOperation) (bool, error) {
		query := url.Values{"enableFinalBackup": {"true"}, "finalBackupTtlDays": {strconv.Itoa(manageddb.CloudSQLFinalBackupDays)}}
		err := s.do(ctx, http.MethodDelete, s.sqlPath("/instances/"+url.PathEscape(name))+"?"+query.Encode(), nil, op)
		if IsNotFound(err) {
			return true, nil
		}
		return false, err
	}, "DELETE")
}

package gcp

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// SQLDeletedPITRSource identifies retained logs independently of a reused name.
// Its final backup must still prove the original instance and isolated settings.
type SQLDeletedPITRSource struct {
	Backup    string
	DeletedAt time.Time
}

type sqlDeletedPITRWitness struct {
	Source                          SQLDeletedPITRSource
	Digest                          string
	Created, BackupStart, BackupEnd time.Time
}

func (s *Session) readSQLDeletedPITRSource(ctx context.Context, region, name, physical, reference string, spec SQLInstanceSpec) (*sqlDeletedPITRWitness, error) {
	if err := s.checkSQLBackupSource(region, name, physical); err != nil {
		return nil, err
	}
	if spec.Name != name || spec.Region != region {
		return nil, ErrSQLPITRUnavailable
	}
	id, err := s.sqlBackupID(reference)
	if err != nil {
		return nil, err
	}
	var backup sqlBackup
	if readErr := s.do(ctx, http.MethodGet, s.sqlPath("/backups/"+url.PathEscape(id)), nil, &backup); readErr != nil {
		return nil, readErr
	}
	point, partial, err := s.sqlBackupPoint(backup, region, name, physical)
	if err != nil {
		return nil, err
	}
	if partial || point == nil || backup.Name != reference || point.Type != "FINAL" || point.State != "SUCCESSFUL" || point.DatabaseVersion != "POSTGRES_17" || point.ExpiresAt != nil && !point.ExpiresAt.After(time.Now()) || backup.DeletedAt == nil || backup.DeletedAt.IsZero() || backup.DeletedAt.After(time.Now()) || backup.Interval.End.After(*backup.DeletedAt) {
		return nil, ErrSQLPITRUnavailable
	}
	created, err := time.Parse(time.RFC3339Nano, backup.Settings.CreateTime)
	if err != nil || !backup.DeletedAt.After(created) {
		return nil, ErrSQLPITRUnavailable
	}
	digest, err := backup.Settings.PITRIsolation(spec)
	if err != nil {
		return nil, err
	}
	return &sqlDeletedPITRWitness{Source: SQLDeletedPITRSource{Backup: reference, DeletedAt: backup.DeletedAt.UTC()}, Digest: digest, Created: created.UTC(), BackupStart: backup.Interval.Start.UTC(), BackupEnd: backup.Interval.End.UTC()}, nil
}

// ReadSQLDeletedPITRWindow brackets current log bounds with the retained final
// backup. No live name lookup can substitute a replacement instance's settings.
func (s *Session) ReadSQLDeletedPITRWindow(ctx context.Context, region, name, physical, reference string, spec SQLInstanceSpec) (*SQLPITRWindow, error) {
	witness, err := s.readSQLDeletedPITRSource(ctx, region, name, physical, reference, spec)
	if err != nil {
		return nil, err
	}
	window, err := s.readSQLPITRBounds(ctx, region, name, witness.Created, &witness.Source)
	if err != nil {
		return nil, err
	}
	current, err := s.readSQLDeletedPITRSource(ctx, region, name, physical, reference, spec)
	if err != nil {
		return nil, err
	}
	if *current != *witness {
		return nil, ErrSQLPITRUnavailable
	}
	window.PhysicalID, window.SettingsDigest = physical, witness.Digest
	return window, nil
}

package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"
)

// ErrSQLPITRUnavailable never authorizes a fallback to a public clone or latest.
var ErrSQLPITRUnavailable = errors.New("cloud sql PITR requires a verified PSC-only source and current log window")

// SQLPITRWindow binds rolling bounds to the original source and settings.
// It is an observation, not proof that a copy has been created or verified.
type SQLPITRWindow struct {
	PhysicalID     string
	SettingsDigest string
	CreatedAt      time.Time
	Earliest       time.Time
	Latest         time.Time
}

// PITRIsolation assesses settings inherited by the standard clone API. The
// source must have no inherited consumer endpoint automation or outbound path.
// Existing manual consumer endpoints refer to a different service attachment.
func (in *SQLInstance) PITRIsolation(spec SQLInstanceSpec) (string, error) {
	if err := in.checkPITRBase(spec); err != nil {
		return "", err
	}
	if in.State != "RUNNABLE" {
		return "", ErrSQLPITRUnavailable
	}
	settings := in.Settings
	disk, backup := spec.Storage, settings.Backup
	if disk.RecoveryDays < 1 || disk.RecoveryDays > 7 || backup == nil || !backup.Enabled || !backup.PITREnabled || backup.RecoveryDays != disk.RecoveryDays {
		return "", ErrSQLPITRUnavailable
	}
	network := settings.IPConfiguration
	if len(network.PSC.AutoConnections) != 0 || network.PSC.AutoDNS {
		return "", ErrSQLPITRUnavailable
	}
	projects := slices.Clone(network.PSC.AllowedProjects)
	for _, project := range projects {
		if !validSQLPathID(project) {
			return "", ErrSQLPITRUnavailable
		}
	}
	slices.Sort(projects)
	projects = slices.Compact(projects)
	// Canonicalize the allowlist without modifying the observation. Include the
	// settings revision so changes outside these reviewed fields also invalidate
	// admission rather than silently blessing unreviewed inherited settings.
	raw, err := json.Marshal(struct {
		Protocol                                                        int
		Version, Engine, Tier, DiskType, Edition, Availability, DataAPI string
		DiskGB                                                          int64
		RecoveryDays                                                    int32
		Projects                                                        []string
	}{1, settings.Version, in.DatabaseVersion, settings.Tier, settings.DataDiskType, settings.Edition, settings.Availability, settings.DataAPIAccess, settings.DataDiskSizeGB, backup.RecoveryDays, projects})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

func (s *Session) readSQLPITRSource(ctx context.Context, region, name, physical string, spec SQLInstanceSpec) (*SQLInstance, string, error) {
	if err := s.checkSQLBackupSource(region, name, physical); err != nil {
		return nil, "", err
	}
	if spec.Name != name || spec.Region != region {
		return nil, "", ErrSQLPITRUnavailable
	}
	in, err := s.GetSQLInstance(ctx, name)
	if IsNotFound(err) {
		return nil, "", ErrSQLPITRUnavailable
	}
	if err != nil {
		return nil, "", err
	}
	pin, err := in.Incarnation(s.key.ProjectID, region, name)
	if err != nil || pin != physical {
		return nil, "", ErrSQLPITRUnavailable
	}
	digest, err := in.PITRIsolation(spec)
	return in, digest, err
}

// ReadSQLPITRWindow reads the current window between two original-source
// observations. Neither a reusable name nor cached bounds authorize a clone.
func (s *Session) ReadSQLPITRWindow(ctx context.Context, region, name, physical string, spec SQLInstanceSpec) (*SQLPITRWindow, error) {
	in, digest, err := s.readSQLPITRSource(ctx, region, name, physical, spec)
	if err != nil {
		return nil, err
	}
	var window struct {
		Kind     string    `json:"kind"`
		Earliest time.Time `json:"earliestRecoveryTime"`
		Latest   time.Time `json:"latestRecoveryTime"`
	}
	query := url.Values{"location": {region}}
	if readErr := s.do(ctx, http.MethodGet, s.sqlPath("/instances/"+url.PathEscape(name)+"/getLatestRecoveryTime")+"?"+query.Encode(), nil, &window); readErr != nil {
		return nil, readErr
	}
	created, err := time.Parse(time.RFC3339Nano, in.CreateTime)
	if err != nil || window.Kind != "sql#getLatestRecoveryTime" || window.Earliest.IsZero() || !window.Earliest.Before(window.Latest) || window.Latest.After(time.Now()) || window.Earliest.Before(created) {
		return nil, ErrSQLPITRUnavailable
	}
	_, current, err := s.readSQLPITRSource(ctx, region, name, physical, spec)
	if err != nil {
		return nil, err
	}
	if current != digest {
		return nil, ErrSQLPITRUnavailable
	}
	return &SQLPITRWindow{PhysicalID: physical, SettingsDigest: digest, CreatedAt: created.UTC(), Earliest: window.Earliest.UTC(), Latest: window.Latest.UTC()}, nil
}

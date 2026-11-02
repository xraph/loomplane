package gcp

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// SQLBackup is verified native metadata. CreatedAt is backupInterval.startTime,
// the provider's recovery interval boundary, not a measured export completion.
type SQLBackup struct {
	Name            string
	Type            string
	State           string
	DatabaseVersion string
	CreatedAt       time.Time
	ExpiresAt       *time.Time
	KMSKey          string
}

type sqlBackup struct {
	Name            string     `json:"name"`
	Kind            string     `json:"kind"`
	Instance        string     `json:"instance"`
	Type            string     `json:"type"`
	State           string     `json:"state"`
	DatabaseVersion string     `json:"databaseVersion"`
	KMSKey          string     `json:"kmsKey"`
	ExpiresAt       *time.Time `json:"expiryTime"`
	DeletedAt       *time.Time `json:"instanceDeletionTime"`
	Interval        struct {
		Start time.Time `json:"startTime"`
		End   time.Time `json:"endTime"`
	} `json:"backupInterval"`
	Settings *SQLInstance `json:"instanceSettings"`
}

func (s *Session) sqlBackupID(reference string) (string, error) {
	id, ok := strings.CutPrefix(reference, "projects/"+s.key.ProjectID+"/backups/")
	if !ok || !validSQLPathID(id) {
		return "", errors.New("cloud sql backup reference does not match the original project")
	}
	return id, nil
}

func sqlBackupHasSource(in sqlBackup) bool {
	p := in.Settings
	return p != nil && p.Name != "" && p.Project != "" && p.Region != "" && p.ConnectionName != "" && p.CreateTime != ""
}

// sqlBackupPoint distinguishes an excluded incarnation from missing proof. The
// latter makes the entire scan partial so earlier observations remain present.
func (s *Session) sqlBackupPoint(in sqlBackup, region, instance, physical string) (*SQLBackup, bool, error) {
	if in.Kind != "sql#backup" || !slices.Contains([]string{"AUTOMATED", "ON_DEMAND", "FINAL"}, in.Type) || !slices.Contains([]string{"ENQUEUED", "RUNNING", "FAILED", "SUCCESSFUL", "DELETING", "DELETION_FAILED"}, in.State) {
		return nil, false, errors.New("cloud sql backup has unknown type or state")
	}
	if !sqlBackupHasSource(in) {
		return nil, true, nil
	}
	p := in.Settings
	pin, err := p.Incarnation(s.key.ProjectID, p.Region, in.Instance)
	if err != nil {
		return nil, false, err
	}
	if in.Instance != instance || p.Region != region || pin != physical {
		return nil, false, nil
	}
	created, err := time.Parse(time.RFC3339Nano, p.CreateTime)
	if err != nil {
		return nil, false, err
	}
	if !in.Interval.Start.IsZero() && in.Interval.Start.Before(created) || !in.Interval.End.IsZero() && in.Interval.End.Before(in.Interval.Start) {
		return nil, false, errors.New("cloud sql backup interval does not match the source creation")
	}
	if in.Interval.Start.IsZero() || in.State == "SUCCESSFUL" && in.Interval.End.IsZero() {
		return nil, true, nil
	}
	var expires *time.Time
	if in.ExpiresAt != nil {
		if in.ExpiresAt.IsZero() || !in.ExpiresAt.After(in.Interval.Start) || !in.Interval.End.IsZero() && in.ExpiresAt.Before(in.Interval.End) {
			return nil, false, errors.New("cloud sql backup expiry contradicts its recovery interval")
		}
		utc := in.ExpiresAt.UTC()
		expires = &utc
	}
	if in.DatabaseVersion == "" || in.DatabaseVersion != p.DatabaseVersion && p.DatabaseVersion != "" {
		return nil, false, errors.New("cloud sql backup engine metadata is inconsistent")
	}
	return &SQLBackup{Name: in.Name, Type: in.Type, State: in.State, DatabaseVersion: in.DatabaseVersion, CreatedAt: in.Interval.Start.UTC(), ExpiresAt: expires, KMSKey: in.KMSKey}, false, nil
}

func (s *Session) checkSQLBackupSource(region, instance, physical string) error {
	digest, ok := strings.CutPrefix(physical, "cloud-sql:")
	raw, err := hex.DecodeString(digest)
	if s.key.ProjectID == "" || region == "" || !sqlNativeName(instance) || !ok || err != nil || len(raw) != 32 {
		return errors.New("cloud sql backup inventory requires the original scoped creation pin")
	}
	return nil
}

// ReadSQLBackup revalidates one retained point against that backup's own source
// metadata. It does not list backups or read the current reusable instance name.
func (s *Session) ReadSQLBackup(ctx context.Context, region, instance, physical string, selected SQLBackup) (*SQLBackup, error) {
	if err := s.checkSQLBackupSource(region, instance, physical); err != nil {
		return nil, err
	}
	id, err := s.sqlBackupID(selected.Name)
	if err != nil {
		return nil, err
	}
	if selected.CreatedAt.IsZero() || selected.DatabaseVersion == "" || !slices.Contains([]string{"AUTOMATED", "ON_DEMAND", "FINAL"}, selected.Type) {
		return nil, errors.New("cloud sql selected backup requires retained creation and engine metadata")
	}
	var detail sqlBackup
	if readErr := s.do(ctx, http.MethodGet, s.sqlPath("/backups/"+url.PathEscape(id)), nil, &detail); readErr != nil {
		return nil, fmt.Errorf("read selected cloud sql backup: %w", readErr)
	}
	point, partial, err := s.sqlBackupPoint(detail, region, instance, physical)
	if err != nil {
		return nil, err
	}
	if partial || !sameSQLBackupIdentity(&selected, point) {
		return nil, errors.New("cloud sql selected backup no longer matches its retained identity")
	}
	if point.State != "SUCCESSFUL" || point.ExpiresAt != nil && !point.ExpiresAt.After(time.Now()) {
		return nil, errors.New("cloud sql selected backup is not currently available for recovery")
	}
	return point, nil
}

// ListSQLBackups reads project-owned backups, including deleted-instance points.
// Optional source settings are fetched by canonical backup reference, never by
// the current instance name. Unverifiable points, provider warnings and a ten
// page bound report complete=false and cannot erase retained observations.
func (s *Session) ListSQLBackups(ctx context.Context, region, instance, physical string) ([]SQLBackup, bool, error) {
	if err := s.checkSQLBackupSource(region, instance, physical); err != nil {
		return nil, false, err
	}
	points := []SQLBackup{}
	complete := true
	seen, tokens := map[string]bool{}, map[string]bool{}
	token := ""
	for range 10 {
		tokens[token] = true
		query := url.Values{"pageSize": {"100"}, "filter": {"instance:" + instance}}
		if token != "" {
			query.Set("pageToken", token)
		}
		var page struct {
			Backups  []sqlBackup `json:"backups"`
			Next     string      `json:"nextPageToken"`
			Warnings []struct{}  `json:"warnings"`
		}
		if err := s.do(ctx, http.MethodGet, s.sqlPath("/backups")+"?"+query.Encode(), nil, &page); err != nil {
			return nil, false, fmt.Errorf("read cloud sql backup inventory: %w", err)
		}
		if len(page.Backups) > 100 {
			return nil, false, errors.New("cloud sql backup page exceeds its requested limit")
		}
		if len(page.Warnings) > 0 {
			complete = false
		}
		for _, in := range page.Backups {
			id, err := s.sqlBackupID(in.Name)
			if err != nil {
				return nil, false, err
			}
			if seen[in.Name] {
				return nil, false, errors.New("cloud sql backup inventory repeated a native identity")
			}
			seen[in.Name] = true
			if in.Instance == "" {
				complete = false
				continue
			}
			if !sqlNativeName(in.Instance) {
				return nil, false, errors.New("cloud sql backup has a malformed source instance identity")
			}
			if !sqlBackupHasSource(in) {
				var detail sqlBackup
				if detailErr := s.do(ctx, http.MethodGet, s.sqlPath("/backups/"+url.PathEscape(id)), nil, &detail); detailErr != nil {
					return nil, false, fmt.Errorf("read cloud sql backup source: %w", detailErr)
				}
				if detail.Name != in.Name || detail.Instance != in.Instance {
					return nil, false, errors.New("cloud sql backup details changed native identity")
				}
				in = detail
			}
			point, partial, err := s.sqlBackupPoint(in, region, instance, physical)
			if err != nil {
				return nil, false, err
			}
			if partial {
				complete = false
			}
			if point != nil {
				points = append(points, *point)
			}
		}
		token = page.Next
		if token == "" {
			return points, complete, nil
		}
		if len(token) > 4096 || tokens[token] {
			return nil, false, errors.New("cloud sql backup pagination is invalid or repeated a token")
		}
	}
	return points, false, nil
}

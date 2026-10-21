package recovery

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

const retentionInventoryLimit = 1000

// RetentionInput previews independent full backups only. It never enables
// deletion or changes a schedule. At least one recent point must be kept.
type RetentionInput struct {
	OlderThanDays int `json:"older_than_days" query:"older_than_days"`
	KeepLast      int `json:"keep_last" query:"keep_last"`
}

type RetentionPoint struct {
	BackupID string `json:"backup_id"`
	Bytes    int64  `json:"bytes"`
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason"`
}

type RetentionPreview struct {
	AsOf          time.Time        `json:"as_of"`
	Complete      bool             `json:"complete"`
	EligibleCount int              `json:"eligible_count"`
	EligibleBytes int64            `json:"eligible_bytes"`
	Points        []RetentionPoint `json:"points"`
}

// RetentionEntry includes copy references from the same database snapshot as
// the recovery point. It is selection evidence, not a deletion lease.
type RetentionEntry struct {
	Backup Backup
	InUse  bool
}

type RetentionStore interface {
	RetentionInventory(context.Context, *Backup) ([]RetentionEntry, error)
}

func (s *Service) PreviewRetention(ctx context.Context, p security.Principal, id string, input RetentionInput) (*RetentionPreview, error) {
	anchor, err := s.Store.Get(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if p.OrgID == "" || anchor.OrgID != p.OrgID {
		return nil, security.ErrForbidden
	}
	if checkErr := s.Authz.Require(ctx, p, security.BackupRead, anchor.ProjectID); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := input.Validate(); checkErr != nil {
		return nil, checkErr
	}
	if _, err = retentionScope(anchor); err != nil {
		return nil, err
	}
	store, ok := s.Store.(RetentionStore)
	if !ok {
		return nil, fmt.Errorf("%w: retention inventory is unavailable", ErrInvalid)
	}
	points, err := store.RetentionInventory(ctx, anchor)
	if err != nil {
		return nil, err
	}
	return previewRetention(time.Now().UTC(), input, anchor, points)
}

// Validate bounds the age and minimum count without accessing recovery data.
func (in RetentionInput) Validate() error {
	if in.OlderThanDays < 1 || in.OlderThanDays > 3650 || in.KeepLast < 1 || in.KeepLast > retentionInventoryLimit {
		return fmt.Errorf("%w: retention requires 1-3650 days and 1-1000 retained points", ErrInvalid)
	}
	return nil
}

type recoveryScope struct {
	org, project, environment, name, binding, instance, connection, provider, format string
}

func retentionScope(b *Backup) (recoveryScope, error) {
	source := &b.Source
	var cfg planning.ResourceConfig
	if b.ID == "" || b.OrgID == "" || b.ProjectID == "" || b.EnvironmentID == "" || b.Name == "" || source.ID == "" || source.ExternalID == "" || source.ConnectionID == "" || source.OrgID != b.OrgID || source.ProjectID != b.ProjectID || source.EnvironmentID != b.EnvironmentID || source.Name != b.Name || source.Subject != planning.ResourceSubject || json.Unmarshal(source.Config, &cfg) != nil || !supportsRecovery(&cfg) || cfg.Provider != source.Provider || cfg.Target != source.ConnectionID {
		return recoveryScope{}, fmt.Errorf("%w: retention requires a consistent database recovery source", ErrInvalid)
	}
	// WAL and incremental chains require a separate dependency-aware planner.
	fullFormat := map[string]string{"postgres": "pg-dump-custom-v1", "mysql": "mysqldump-sql-v1", "mariadb": "mariadb-dump-sql-v1", "redis": "redis-rdb-v1", "valkey": "valkey-rdb-v1"}
	if cfg.Catalog.BackupFormat != fullFormat[cfg.Kind] {
		return recoveryScope{}, fmt.Errorf("%w: retention requires independent full backups", ErrInvalid)
	}
	return recoveryScope{b.OrgID, b.ProjectID, b.EnvironmentID, b.Name, source.ID, source.ExternalID, source.ConnectionID, source.Provider, cfg.Catalog.BackupFormat}, nil
}

func usableRetentionPoint(b *Backup, now time.Time) bool {
	if b.Status != "succeeded" || b.CompletedAt == nil || b.CompletedAt.IsZero() || b.CompletedAt.After(now) || b.CreatedAt.IsZero() || b.CreatedAt.After(*b.CompletedAt) || b.Object.Size <= 0 || b.Object.Validate() != nil {
		return false
	}
	return b.Token != "" && b.Object.Key == b.objectKey()
}

func verifiedRetentionPoint(now time.Time, points []RetentionEntry) *Backup {
	var latest *Backup
	for i := range points {
		b := &points[i].Backup
		if !usableRetentionPoint(b, now) || b.RestoredAt == nil || b.RestoredAt.Before(*b.CompletedAt) || b.RestoredAt.After(now) {
			continue
		}
		if latest == nil || b.CompletedAt.After(*latest.CompletedAt) || b.CompletedAt.Equal(*latest.CompletedAt) && b.ID > latest.ID {
			latest = b
		}
	}
	return latest
}

func previewRetention(now time.Time, input RetentionInput, anchor *Backup, inventory []RetentionEntry) (*RetentionPreview, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	preview := &RetentionPreview{AsOf: now, Complete: len(inventory) <= retentionInventoryLimit, Points: []RetentionPoint{}}
	points := slices.Clone(inventory)
	scope, err := retentionScope(anchor)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(points))
	for i := range points {
		current, err := retentionScope(&points[i].Backup)
		if err != nil {
			return nil, err
		}
		if current != scope || seen[points[i].Backup.ID] {
			return nil, fmt.Errorf("%w: retention inventory contains mixed sources or duplicate points", ErrInvalid)
		}
		seen[points[i].Backup.ID] = true
	}
	slices.SortFunc(points, func(a, b RetentionEntry) int {
		var at, bt time.Time
		if a.Backup.CompletedAt != nil {
			at = *a.Backup.CompletedAt
		}
		if b.Backup.CompletedAt != nil {
			bt = *b.Backup.CompletedAt
		}
		if order := bt.Compare(at); order != 0 {
			return order
		}
		return strings.Compare(b.Backup.ID, a.Backup.ID)
	})
	verified := ""
	if witness := verifiedRetentionPoint(now, points); witness != nil {
		verified = witness.ID
	}
	cutoff, kept := now.Add(-time.Duration(input.OlderThanDays)*24*time.Hour), 0
	for _, point := range points {
		b := &point.Backup
		usable := usableRetentionPoint(b, now)
		if usable {
			kept++
		}
		reason := "eligible"
		switch {
		case !preview.Complete:
			reason = "inventory_incomplete"
		case !usable:
			reason = "not_complete"
		case point.InUse:
			reason = "active_restore"
		case kept <= input.KeepLast:
			reason = "newest"
		case b.ID == verified:
			reason = "last_verified"
		case verified == "":
			reason = "no_verified_backup"
		case !b.CompletedAt.Before(cutoff):
			reason = "recent"
		}
		p := RetentionPoint{BackupID: b.ID, Bytes: b.Object.Size, Eligible: reason == "eligible", Reason: reason}
		preview.Points = append(preview.Points, p)
		if p.Eligible {
			preview.EligibleCount++
			preview.EligibleBytes += p.Bytes
		}
	}
	return preview, nil
}

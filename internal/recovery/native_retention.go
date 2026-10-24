package recovery

import (
	"cmp"
	"slices"
	"time"
)

// NativeRetentionEntry joins native inventory with durable copy and deletion
// references. Provider availability alone does not establish SQL recovery proof.
type NativeRetentionEntry struct {
	Point            NativePoint
	InUse            bool
	Retired          bool
	WitnessProtected bool
	VerifiedAt       *time.Time
}

func previewNativeRetention(now time.Time, provider string, input RetentionInput, entries []NativeRetentionEntry, complete bool) (*RetentionPreview, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	if provider != "rds" && provider != "cloud-sql" {
		return nil, ErrInvalid
	}
	entries = slices.Clone(entries)
	slices.SortFunc(entries, func(a, b NativeRetentionEntry) int {
		if n := b.Point.CreatedAt.Compare(a.Point.CreatedAt); n != 0 {
			return n
		}
		return cmp.Compare(b.Point.ID, a.Point.ID)
	})
	complete = complete && len(entries) <= retentionInventoryLimit
	if len(entries) > retentionInventoryLimit {
		entries = entries[:retentionInventoryLimit]
	}
	out := &RetentionPreview{AsOf: now, Complete: complete, Points: []RetentionPoint{}}
	var witness *NativePoint
	for i := range entries {
		entry := &entries[i]
		if !usableNativeRetentionPoint(now, provider, entry.Point) || entry.Retired || entry.VerifiedAt == nil || entry.VerifiedAt.Before(entry.Point.CreatedAt) || entry.VerifiedAt.After(now) {
			continue
		}
		witness = &entry.Point
		out.VerifiedBackupID = entry.Point.ID
		break
	}
	kept := 0
	for _, entry := range entries {
		point := entry.Point
		supported := nativePointDeletionSupported(provider, point)
		usable := usableNativeRetentionPoint(now, provider, point)
		if usable && !entry.Retired {
			kept++
		}
		item := RetentionPoint{BackupID: point.ID, CreatedAt: point.CreatedAt, DeletionSupported: supported}
		switch {
		case !supported:
			item.Reason = "provider_managed"
			item.DeletionUnavailableReason = "Provider-managed recovery chains are not pruned individually."
		case !complete:
			item.Reason = "incomplete_inventory"
		case !usable:
			item.Reason = "unavailable"
		case entry.Retired:
			item.Reason = "deletion_admitted"
		case entry.InUse:
			item.Reason = "in_use"
		case entry.WitnessProtected:
			item.Reason = "witness_in_use"
		case kept <= input.KeepLast:
			item.Reason = "keep_last"
		case !point.CreatedAt.Before(now.Add(-time.Duration(input.OlderThanDays) * 24 * time.Hour)):
			item.Reason = "too_recent"
		case witness == nil || !point.CreatedAt.Before(witness.CreatedAt):
			item.Reason = "no_newer_verified_backup"
		default:
			item.Eligible = true
			item.Reason = "eligible"
			out.EligibleCount++
		}
		out.Points = append(out.Points, item)
	}
	return out, nil
}

func nativePointDeletionSupported(provider string, p NativePoint) bool {
	return provider == "rds" && p.Kind == "manual" || provider == "cloud-sql" && (p.Kind == "ON_DEMAND" || p.Kind == "FINAL")
}
func usableNativeRetentionPoint(now time.Time, provider string, p NativePoint) bool {
	status := "available"
	if provider == "cloud-sql" {
		status = "SUCCESSFUL"
	}
	return nativePointDeletionSupported(provider, p) && p.ID != "" && p.Present && p.Status == status && !p.CreatedAt.IsZero() && !p.CreatedAt.After(now) && (p.ExpiresAt == nil || p.ExpiresAt.After(now))
}

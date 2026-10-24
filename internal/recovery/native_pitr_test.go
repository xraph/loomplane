package recovery

import (
	"context"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/security"
)

func TestNativePITRAdmissionRechecksExactTimeAgainstRollingWindow(t *testing.T) {
	for _, scenario := range []string{"covered", "rolled-past", "missing-time", "changed-source", "unknown-protocol"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			at := now.Add(-24 * time.Hour)
			point := NativePoint{ID: "point", SourceID: "source", ProviderID: "native-source", Name: "database", Kind: "pitr", CreatedAt: now.Add(-30 * 24 * time.Hour), Engine: "postgres", Version: "17.2", Encrypted: true, PITR: &NativePITRBounds{Protocol: 1, Earliest: now.Add(-7 * 24 * time.Hour), Latest: now.Add(-time.Minute)}}
			fresh := cloneNativePoint(point)
			fresh.Status = "available"
			fresh.PITR.Earliest = fresh.PITR.Earliest.Add(time.Hour)
			selected := &at
			switch scenario {
			case "rolled-past":
				fresh.PITR.Earliest = at.Add(time.Second)
			case "missing-time":
				selected = nil
			case "changed-source":
				fresh.CreatedAt = fresh.CreatedAt.Add(time.Nanosecond)
			case "unknown-protocol":
				fresh.PITR.Protocol = 2
			}
			store := &nativeSelectedStore{source: &NativeSource{ID: "source", OrgID: "org", ProjectID: "project", Provider: "rds"}, point: &point}
			backend := &nativeSelectedBackend{point: &fresh}
			service := &NativeService{Store: store, Backend: backend, Authz: allowed{}, Projects: fixedProjects{}}
			checker, ok := any(service).(interface {
				CheckPointAt(context.Context, security.Principal, string, string, *time.Time) (*NativePoint, error)
			})
			if !ok {
				t.Fatal("PITR admission cannot retain an explicit reviewed time")
			}
			result, err := checker.CheckPointAt(t.Context(), security.Principal{OrgID: "org"}, "source", "point", selected)
			if scenario == "covered" {
				if err != nil || result.RestoreTime == nil || !result.RestoreTime.Equal(at) || !result.PITR.Earliest.Equal(fresh.PITR.Earliest) {
					t.Fatal("covered time lost its exact selection or fresh bounds", result, err)
				}
			} else if err == nil {
				t.Fatal("invalid PITR selection reached admission", scenario, result)
			}
			if scenario == "missing-time" && backend.calls != 0 {
				t.Fatal("implicit latest time reached provider")
			}
		})
	}
}

type nativePointTimeSelector interface {
	SelectTime(string, *time.Time) (NativePoint, error)
}

func TestNativePITRSelectionKeepsExplicitTimeAndSnapshotMode(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, scenario := range []string{"snapshot", "snapshot-time", "pitr", "earliest", "latest", "missing-time", "offset", "rds-fractional", "google-fractional", "unknown-protocol", "missing-bounds", "future-bounds"} {
		t.Run(scenario, func(t *testing.T) {
			point := NativePoint{Kind: "pitr", PITR: &NativePITRBounds{Protocol: 1, Earliest: now.Add(-7 * 24 * time.Hour), Latest: now.Add(-time.Minute)}}
			at := now.Add(-24 * time.Hour)
			selected := &at
			provider := "rds"
			switch scenario {
			case "snapshot":
				point.Kind = "manual"
				point.PITR = nil
				selected = nil
			case "snapshot-time":
				point.Kind = "manual"
				point.PITR = nil
			case "earliest":
				at = point.PITR.Earliest
			case "latest":
				at = point.PITR.Latest
			case "missing-time":
				selected = nil
			case "offset":
				at = at.In(time.FixedZone("client-local", 3600))
			case "rds-fractional":
				at = at.Add(time.Nanosecond)
			case "google-fractional":
				provider = "cloud-sql"
				at = at.Add(time.Nanosecond)
			case "unknown-protocol":
				point.PITR.Protocol = 2
			case "missing-bounds":
				point.PITR = nil
			case "future-bounds":
				point.PITR.Latest = now.Add(time.Hour)
			}
			selector, ok := any(point).(nativePointTimeSelector)
			if !ok {
				t.Fatal("native PITR has no explicit time selection contract")
			}
			result, err := selector.SelectTime(provider, selected)
			allowed := scenario == "snapshot" || scenario == "pitr" || scenario == "earliest" || scenario == "google-fractional"
			if allowed != (err == nil) {
				t.Fatal("native selection changed recovery mode or UTC time", result, err)
			}
			if err == nil && selected != nil && (result.RestoreTime == nil || !result.RestoreTime.Equal(at)) {
				t.Fatal("native selection rounded or replaced the explicit time", result)
			}
			if err == nil && result.RestoreTime != nil {
				at = at.Add(time.Second)
				if result.RestoreTime.Equal(at) {
					t.Fatal("selected time aliases mutable caller input")
				}
			}
		})
	}
}
func TestNativePITRRequestReplayCannotChangeSelectedTime(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	first, second := now.Add(-24*time.Hour), now.Add(-23*time.Hour)
	point := NativePoint{ID: "point", SourceID: "source", ProviderID: "native-source", Name: "database", Kind: "pitr", CreatedAt: now.Add(-30 * 24 * time.Hour), Engine: "postgres", Version: "17.2", Encrypted: true, PITR: &NativePITRBounds{Protocol: 1, Earliest: now.Add(-7 * 24 * time.Hour), Latest: now.Add(-time.Minute)}, RestoreTime: &first}
	a := &NativeRestore{Point: point}
	b := *a
	b.Point.RestoreTime = &second
	if sameNativeRestoreRequest(a, &b) {
		t.Fatal("native request replay silently changed the selected PITR time")
	}
	b.Point.RestoreTime = &first
	b.Point.PITR = &NativePITRBounds{Protocol: 1, Earliest: point.PITR.Earliest.Add(time.Hour), Latest: point.PITR.Latest.Add(time.Second)}
	if !sameNativeRestoreRequest(a, &b) {
		t.Fatal("a rolling observation changed an existing request's identity")
	}
	copied := copyNativeRestore(*a)
	copied.Point.PITR.Earliest = copied.Point.PITR.Earliest.Add(time.Hour)
	*copied.Point.RestoreTime = copied.Point.RestoreTime.Add(time.Second)
	if !a.Point.PITR.Earliest.Equal(now.Add(-7*24*time.Hour)) || !a.Point.RestoreTime.Equal(now.Add(-24*time.Hour)) {
		t.Fatal("worker input mutation changed the retained PITR request")
	}
}

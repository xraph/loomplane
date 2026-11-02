package recovery

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/security"
)

func cloudSQLPITRPoint() NativePoint {
	now := time.Now().UTC().Truncate(time.Second)
	at := now.Add(-time.Hour)
	return NativePoint{ID: "point", SourceID: "source", ProviderID: "original-instance", Name: "database", Kind: "pitr", Status: "available", Engine: "postgres", Version: "17", CreatedAt: now.Add(-30 * 24 * time.Hour), Encrypted: true, RestoreTime: &at, PITR: &NativePITRBounds{Protocol: 1, Earliest: now.Add(-7 * 24 * time.Hour), Latest: now.Add(-time.Minute), SettingsDigest: strings.Repeat("a", 64)}}
}

func TestCloudSQLPITRSelectionRequiresCanonicalSettingsWitness(t *testing.T) {
	for _, digest := range []string{"", "a", strings.Repeat("A", 64), strings.Repeat("g", 64), strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("a", 64)} {
		t.Run(digest, func(t *testing.T) {
			point := cloudSQLPITRPoint()
			point.PITR.SettingsDigest = digest
			_, err := point.SelectTime("cloud-sql", point.RestoreTime)
			if valid := digest == strings.Repeat("a", 64); valid != (err == nil) {
				t.Fatalf("settings witness validity %t, selection error %v", valid, err)
			}
		})
	}
	point := cloudSQLPITRPoint()
	if _, err := point.SelectTime("rds", point.RestoreTime); err == nil {
		t.Fatal("Cloud SQL settings witness accepted for an RDS selection")
	}
}

func TestCloudSQLPITRAdmissionRejectsChangedSettings(t *testing.T) {
	for _, scenario := range []string{"rolling-window", "changed-settings", "missing-settings"} {
		t.Run(scenario, func(t *testing.T) {
			selected := cloudSQLPITRPoint()
			fresh := cloneNativePoint(selected)
			fresh.RestoreTime = nil
			fresh.PITR.Latest = fresh.PITR.Latest.Add(time.Second)
			switch scenario {
			case "changed-settings":
				fresh.PITR.SettingsDigest = strings.Repeat("b", 64)
			case "missing-settings":
				fresh.PITR.SettingsDigest = ""
			}
			store := &nativeSelectedStore{source: &NativeSource{ID: "source", OrgID: "org", ProjectID: "project", Provider: "cloud-sql"}, point: &selected}
			backend := &nativeSelectedBackend{point: &fresh}
			service := &NativeService{Store: store, Backend: backend, Authz: allowed{}, Projects: fixedProjects{}}
			got, err := service.CheckPointAt(t.Context(), security.Principal{OrgID: "org"}, "source", "point", selected.RestoreTime)
			if scenario != "rolling-window" {
				if err == nil {
					t.Fatal("a new settings observation replaced reviewed clone isolation")
				}
				return
			}
			if err != nil || got.PITR.SettingsDigest != selected.PITR.SettingsDigest || !got.PITR.Latest.Equal(fresh.PITR.Latest) {
				t.Fatal("rolling window lost its settings witness", got, err)
			}
		})
	}
}

func TestCloudSQLPITRReviewAndReplayRetainSettings(t *testing.T) {
	point := cloudSQLPITRPoint()
	principal := security.Principal{OrgID: "org", UserID: "user"}
	service := &NativeService{ReviewKey: []byte(strings.Repeat("test-key", 8))}
	review := nativeReviewClaims{Version: 1, RequestID: "settings-review", OrgID: "org", UserID: "user", ProjectID: "project", EnvironmentID: "environment", SourceID: "source", Point: point, TTLHours: 1, ValidUntil: time.Now().Add(time.Minute), Target: NativeRestoreTarget{CloudID: "cloud", TargetID: "target", CloudRevision: 1, TargetRevision: 1, GrantRevision: 1}}
	raw, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	signature := base64.RawURLEncoding.EncodeToString(service.signNativeReview(payload))
	claims, err := service.readNativeReview(principal, payload+"."+signature)
	if err != nil || claims.Point.PITR.SettingsDigest != point.PITR.SettingsDigest {
		t.Fatal("signed review payload lost the settings witness", err)
	}
	tampered := strings.Replace(string(raw), point.PITR.SettingsDigest, strings.Repeat("b", 64), 1)
	if _, err = service.readNativeReview(principal, base64.RawURLEncoding.EncodeToString([]byte(tampered))+"."+signature); err == nil {
		t.Fatal("changed settings retained the original review signature")
	}
	original := &NativeRestore{Point: claims.Point}
	retry := copyNativeRestore(*original)
	retry.Point.PITR.Latest = retry.Point.PITR.Latest.Add(time.Second)
	if !sameNativeRestoreRequest(original, &retry) {
		t.Fatal("rolling bounds changed request identity")
	}
	retry.Point.PITR.SettingsDigest = strings.Repeat("b", 64)
	if sameNativeRestoreRequest(original, &retry) || original.Point.PITR.SettingsDigest != point.PITR.SettingsDigest {
		t.Fatal("retry changed reviewed settings or mutated the original request")
	}
}

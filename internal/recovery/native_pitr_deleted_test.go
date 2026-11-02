package recovery

import (
	"encoding/json"
	"testing"
	"time"
)

func deletedCloudSQLPoint() NativePoint {
	point := cloudSQLPITRPoint()
	deleted := point.PITR.Latest.Add(time.Second)
	point.PITR.SourceBackup = "projects/source-project/backups/final-original"
	point.PITR.SourceDeletedAt = &deleted
	return point
}

func TestDeletedCloudSQLPITRRequiresCompleteDeletionContext(t *testing.T) {
	for _, scenario := range []string{"valid", "missing-backup", "missing-time", "malformed-backup", "future", "before-created", "logs-after-deletion", "rds"} {
		t.Run(scenario, func(t *testing.T) {
			point := deletedCloudSQLPoint()
			provider := "cloud-sql"
			switch scenario {
			case "missing-backup":
				point.PITR.SourceBackup = ""
			case "missing-time":
				point.PITR.SourceDeletedAt = nil
			case "malformed-backup":
				point.PITR.SourceBackup = "projects/source-project/backups/../another"
			case "future":
				*point.PITR.SourceDeletedAt = time.Now().Add(time.Hour)
			case "before-created":
				*point.PITR.SourceDeletedAt = point.CreatedAt.Add(-time.Second)
			case "logs-after-deletion":
				*point.PITR.SourceDeletedAt = point.PITR.Latest.Add(-time.Second)
			case "rds":
				provider = "rds"
				point.PITR.SettingsDigest = ""
			}
			_, err := point.SelectTime(provider, point.RestoreTime)
			if (err == nil) != (scenario == "valid") {
				t.Fatal("unexpected deleted source admission", scenario, err)
			}
		})
	}
}

func TestDeletedCloudSQLPITRSelectionCannotChangeItsDeletionWitness(t *testing.T) {
	point := deletedCloudSQLPoint()
	for _, scenario := range []string{"rolling", "backup", "time", "live"} {
		t.Run(scenario, func(t *testing.T) {
			selected := cloneNativePoint(point)
			selected.PITR.Earliest = selected.PITR.Earliest.Add(time.Second)
			switch scenario {
			case "backup":
				selected.PITR.SourceBackup = "projects/source-project/backups/another"
			case "time":
				*selected.PITR.SourceDeletedAt = selected.PITR.SourceDeletedAt.Add(time.Nanosecond)
			case "live":
				selected.PITR.SourceBackup, selected.PITR.SourceDeletedAt = "", nil
			}
			if sameNativePointSelection(point, selected) != (scenario == "rolling") {
				t.Fatal("request identity changed its deleted source", scenario)
			}
			if !point.PITR.SourceDeletedAt.Equal(point.PITR.Latest.Add(time.Second)) {
				t.Fatal("selection clone mutated the original deletion timestamp")
			}
		})
	}
	var roundTrip NativePoint
	raw, err := json.Marshal(point)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &roundTrip); err != nil || !sameNativePointSelection(point, roundTrip) {
		t.Fatal("JSON lost deletion context", err)
	}
}

func TestDeletedCloudSQLPITRCopyReviewPinsBackupProject(t *testing.T) {
	for _, project := range []string{"source-project", "other-project"} {
		source, point, target := cloudSQLPITRReviewFixture(t)
		deleted := point.PITR.Latest.Add(time.Second)
		point.PITR.SourceDeletedAt = &deleted
		point.PITR.SourceBackup = "projects/" + project + "/backups/final-original"
		_, err := NativeSQLCopyConfig(source, point, target)
		if (err == nil) != (project == "source-project") {
			t.Fatal("deleted backup crossed the source project", project, err)
		}
	}
}

package recovery

import (
	"testing"
	"time"
)

func TestNativeRetentionKeepsVerifiedWitnessAndIndependentPoints(t *testing.T) {
	now := time.Now().UTC()
	proof := now.Add(-time.Hour)
	entries := []NativeRetentionEntry{
		{Point: NativePoint{ID: "old", Kind: "manual", Status: "available", CreatedAt: now.Add(-30 * 24 * time.Hour), Present: true}},
		{Point: NativePoint{ID: "witness", Kind: "manual", Status: "available", CreatedAt: now.Add(-20 * 24 * time.Hour), Present: true}, VerifiedAt: &proof},
		{Point: NativePoint{ID: "new", Kind: "manual", Status: "available", CreatedAt: now.Add(-24 * time.Hour), Present: true}},
		{Point: NativePoint{ID: "automated", Kind: "automated", Status: "available", CreatedAt: now.Add(-25 * 24 * time.Hour), Present: true}},
	}
	for _, scenario := range []string{"complete", "boundary", "partial", "in-use", "retired", "witness-protected", "no-proof", "expired-witness", "future-proof", "keep-all", "cloud-sql"} {
		t.Run(scenario, func(t *testing.T) {
			copyEntries := append([]NativeRetentionEntry(nil), entries...)
			provider, keep := "rds", 1
			switch scenario {
			case "boundary":
				copyEntries[0].Point.CreatedAt = now.Add(-7 * 24 * time.Hour)
				copyEntries[1].Point.CreatedAt = now.Add(-2 * 24 * time.Hour)
			case "in-use":
				copyEntries[0].InUse = true
			case "retired":
				copyEntries[0].Retired = true
			case "witness-protected":
				copyEntries[0].WitnessProtected = true
			case "no-proof":
				copyEntries[1].VerifiedAt = nil
			case "expired-witness":
				past := now.Add(-time.Second)
				copyEntries[1].Point.ExpiresAt = &past
			case "future-proof":
				future := now.Add(time.Hour)
				copyEntries[1].VerifiedAt = &future
			case "keep-all":
				keep = 3
			case "cloud-sql":
				provider = "cloud-sql"
				for i := range copyEntries {
					copyEntries[i].Point.Kind = "FINAL"
					copyEntries[i].Point.Status = "SUCCESSFUL"
				}
				copyEntries[3].Point.Kind = "AUTOMATED"
			}
			preview, err := previewNativeRetention(now, provider, RetentionInput{OlderThanDays: 7, KeepLast: keep}, copyEntries, scenario != "partial")
			if err != nil || preview == nil {
				t.Fatal("native retention preview missing", err)
			}
			want := 0
			if scenario == "complete" || scenario == "cloud-sql" {
				want = 1
			}
			if preview.EligibleCount != want {
				t.Fatal("unsafe native retention selection", preview)
			}
			if want == 1 && preview.VerifiedBackupID != "witness" {
				t.Fatal("native retention lost SQL-verified witness")
			}
			for _, p := range preview.Points {
				if p.Eligible && p.BackupID != "old" {
					t.Fatal("protected point selected", p)
				}
			}
		})
	}
}

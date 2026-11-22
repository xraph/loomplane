package manageddb

import "testing"

func TestReviewedSizePreservesBackupAndNetworkSettings(t *testing.T) {
	for _, tc := range []struct{ target, class string }{{"rds", "db.t4g.small"}, {"cloud-sql", "db-custom-2-7680"}} {
		t.Run(tc.target, func(t *testing.T) {
			base, err := Postgres(tc.target, "17")
			if err != nil {
				t.Fatal(err)
			}
			values := map[string]string{"compute_class": tc.class}
			sized, err := SizedProfile(*base, values)
			if err != nil || sized.ComputeClass != tc.class || !MatchesProfile(sized, *base, values) {
				t.Fatalf("size: %+v, %v", sized, err)
			}
			sized.BackupRetentionValue++
			if MatchesProfile(sized, *base, values) {
				t.Fatal("capacity accepted a change to backup retention")
			}
			if _, err = SizedProfile(*base, map[string]string{"compute_class": "unknown"}); err == nil {
				t.Fatal("unknown capacity accepted")
			}
		})
	}
}

package aws

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type dbPITRReader interface {
	ReadDBPITRWindow(context.Context, string) (*DBPITRWindow, error)
}

func TestRDSPITRWindowUsesPhysicalSourceAndProviderBounds(t *testing.T) {
	for _, scenario := range []string{"active", "retained", "empty", "no-window", "foreign-source", "foreign-account", "foreign-region", "missing-creation", "unknown-encryption", "missing-key", "future-window", "reversed-window", "before-source", "duplicate", "partial", "denied"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			created := now.Add(-30 * 24 * time.Hour).Add(123456789 * time.Nanosecond)
			earliest, latest := now.Add(-7*24*time.Hour), now.Add(-time.Minute)
			physical, account, region := "db-original", "123456789012", "us-east-1"
			if scenario == "foreign-source" {
				physical = "db-replacement"
			}
			if scenario == "foreign-account" {
				account = "999999999999"
			}
			if scenario == "foreign-region" {
				region = "us-west-2"
			}
			if scenario == "future-window" {
				latest = now.Add(time.Hour)
			}
			if scenario == "reversed-window" {
				earliest = latest.Add(time.Second)
			}
			if scenario == "before-source" {
				earliest = created.Add(-time.Second)
			}
			observedCreated := created.Format(time.RFC3339Nano)
			if scenario == "missing-creation" {
				observedCreated = ""
			}
			encryption := "<Encrypted>true</Encrypted>"
			if scenario == "unknown-encryption" {
				encryption = ""
			}
			key := "original-key"
			if scenario == "missing-key" {
				key = ""
			}
			status := "active"
			if scenario == "retained" {
				status = "retained"
			}
			window := fmt.Sprintf("<RestoreWindow><EarliestTime>%s</EarliestTime><LatestTime>%s</LatestTime></RestoreWindow>", earliest.Format(time.RFC3339), latest.Format(time.RFC3339))
			if scenario == "no-window" {
				window = ""
				status = "creating"
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.Form.Get("Action") != "DescribeDBInstanceAutomatedBackups" || r.Form.Get("DbiResourceId") != "db-original" || r.Form.Get("DBInstanceIdentifier") != "" {
					t.Error("PITR read fell back to a reusable source name", r.Form)
				}
				w.Header().Set("Content-Type", "text/xml")
				if scenario == "denied" {
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(w, `<ErrorResponse><Error><Code>AccessDenied</Code></Error></ErrorResponse>`)
					return
				}
				point := fmt.Sprintf(`<DBInstanceAutomatedBackup><DBInstanceArn>arn:aws:rds:%s:%s:db:database</DBInstanceArn><DBInstanceIdentifier>database</DBInstanceIdentifier><DbiResourceId>%s</DbiResourceId><InstanceCreateTime>%s</InstanceCreateTime><Engine>postgres</Engine><EngineVersion>17.2</EngineVersion><Status>%s</Status>%s<KmsKeyId>%s</KmsKeyId><AllocatedStorage>20</AllocatedStorage><BackupRetentionPeriod>7</BackupRetentionPeriod><MasterUsername>app</MasterUsername>%s</DBInstanceAutomatedBackup>`, region, account, physical, observedCreated, status, encryption, key, window)
				if scenario == "empty" {
					point = ""
				}
				if scenario == "duplicate" {
					point += point
				}
				marker := ""
				if scenario == "partial" {
					marker = "<Marker>more</Marker>"
				}
				fmt.Fprint(w, `<DescribeDBInstanceAutomatedBackupsResponse><DescribeDBInstanceAutomatedBackupsResult><DBInstanceAutomatedBackups>`+point+`</DBInstanceAutomatedBackups>`+marker+`</DescribeDBInstanceAutomatedBackupsResult></DescribeDBInstanceAutomatedBackupsResponse>`)
			}))
			defer server.Close()
			reader, ok := any(New(server.URL).Session(&Key{AccessKeyID: "AKIATESTTESTTESTTEST", SecretAccessKey: "test-secret-test-secret"}, "us-east-1", "123456789012")).(dbPITRReader)
			if !ok {
				t.Fatal("RDS has no original-source PITR window reader")
			}
			windowRead, err := reader.ReadDBPITRWindow(t.Context(), "db-original")
			if scenario != "active" && scenario != "retained" {
				if err == nil || windowRead != nil {
					t.Fatal("unproven PITR window was exposed", windowRead, err)
				}
				return
			}
			if err != nil || windowRead == nil || windowRead.ResourceID != "db-original" || !windowRead.CreatedAt.Equal(created) || !windowRead.Earliest.Equal(earliest) || !windowRead.Latest.Equal(latest) || !windowRead.EncryptionKnown || !windowRead.Encrypted || windowRead.KMSKey != "original-key" || windowRead.StorageGB != 20 || windowRead.MasterUser != "app" || calls != 1 {
				t.Fatal("PITR window lost provider evidence", windowRead, calls, err)
			}
			if strings.Contains(windowRead.ARN, "snapshot:") {
				t.Fatal("PITR window was represented as a snapshot")
			}
		})
	}
}

func TestRDSPITRTimeRequiresExplicitUTCBoundedSecond(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	base := DBPITRWindow{Earliest: now.Add(-7 * 24 * time.Hour), Latest: now.Add(-time.Minute)}
	for _, scenario := range []string{"earliest", "inside", "latest", "older", "zero", "fractional", "offset", "unknown-earliest", "reversed", "future-window"} {
		t.Run(scenario, func(t *testing.T) {
			window := base
			at := now.Add(-24 * time.Hour)
			switch scenario {
			case "earliest":
				at = base.Earliest
			case "latest":
				at = base.Latest
			case "older":
				at = base.Earliest.Add(-time.Second)
			case "zero":
				at = time.Time{}
			case "fractional":
				at = at.Add(time.Nanosecond)
			case "offset":
				at = at.In(time.FixedZone("client-local", 2*60*60))
			case "unknown-earliest":
				window.Earliest = time.Time{}
			case "reversed":
				window.Earliest = window.Latest
			case "future-window":
				window.Latest = now.Add(time.Hour)
			}
			validator, ok := any(window).(interface{ CheckRestoreTime(time.Time) error })
			if !ok {
				t.Fatal("PITR has no explicit bounded UTC-time contract")
			}
			err := validator.CheckRestoreTime(at)
			allowed := scenario == "earliest" || scenario == "inside"
			if allowed != (err == nil) {
				t.Fatal("PITR changed the requested timestamp or accepted an invalid boundary", at, err)
			}
		})
	}
}

type dbPITRSelectedReader interface {
	ReadSelectedDBPITRWindow(context.Context, string, DBPITRWindow, time.Time) (*DBPITRWindow, error)
}

func TestRDSPITRSelectionRechecksRollingWindowAndOriginalEpoch(t *testing.T) {
	for _, scenario := range []string{"rolling", "retained", "expired", "replacement", "changed-key", "denied", "wrong-selected-source", "latest"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			original := DBPITRWindow{ARN: "arn:aws:rds:us-east-1:123456789012:db:database", InstanceID: "database", ResourceID: "db-original", Engine: "postgres", EngineVersion: "17.2", Encrypted: true, EncryptionKnown: true, KMSKey: "original-key", CreatedAt: now.Add(-30 * 24 * time.Hour), Earliest: now.Add(-7 * 24 * time.Hour), Latest: now.Add(-time.Minute), StorageGB: 20, MasterUser: "app"}
			selected, fresh := original, original
			fresh.Latest = now.Add(-10 * time.Second)
			fresh.Status = "active"
			at := now.Add(-3 * 24 * time.Hour)
			switch scenario {
			case "retained":
				fresh.Status = "retained"
			case "expired":
				fresh.Earliest = now.Add(-2 * 24 * time.Hour)
			case "replacement":
				fresh.CreatedAt = fresh.CreatedAt.Add(time.Nanosecond)
			case "changed-key":
				fresh.KMSKey = "replacement-key"
			case "wrong-selected-source":
				selected.ResourceID = "db-other"
			case "latest":
				at = selected.Latest
			}
			reads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reads++
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.Form.Get("Action") != "DescribeDBInstanceAutomatedBackups" || r.Form.Get("DbiResourceId") != "db-original" {
					t.Error("PITR selection lost original identity", r.Form)
				}
				w.Header().Set("Content-Type", "text/xml")
				if scenario == "denied" {
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(w, `<ErrorResponse><Error><Code>AccessDenied</Code></Error></ErrorResponse>`)
					return
				}
				fmt.Fprintf(w, `<DescribeDBInstanceAutomatedBackupsResponse><DescribeDBInstanceAutomatedBackupsResult><DBInstanceAutomatedBackups><DBInstanceAutomatedBackup><DBInstanceArn>%s</DBInstanceArn><DBInstanceIdentifier>%s</DBInstanceIdentifier><DbiResourceId>%s</DbiResourceId><InstanceCreateTime>%s</InstanceCreateTime><Engine>postgres</Engine><EngineVersion>17.2</EngineVersion><Encrypted>true</Encrypted><KmsKeyId>%s</KmsKeyId><Status>%s</Status><AllocatedStorage>20</AllocatedStorage><MasterUsername>app</MasterUsername><RestoreWindow><EarliestTime>%s</EarliestTime><LatestTime>%s</LatestTime></RestoreWindow></DBInstanceAutomatedBackup></DBInstanceAutomatedBackups></DescribeDBInstanceAutomatedBackupsResult></DescribeDBInstanceAutomatedBackupsResponse>`, fresh.ARN, fresh.InstanceID, fresh.ResourceID, fresh.CreatedAt.Format(time.RFC3339Nano), fresh.KMSKey, fresh.Status, fresh.Earliest.Format(time.RFC3339Nano), fresh.Latest.Format(time.RFC3339Nano))
			}))
			defer server.Close()
			reader, ok := any(New(server.URL).Session(&Key{AccessKeyID: "AKIATESTTESTTESTTEST", SecretAccessKey: "test-secret-test-secret"}, "us-east-1", "123456789012")).(dbPITRSelectedReader)
			if !ok {
				t.Fatal("PITR selection cannot recheck the original epoch and rolling window")
			}
			result, err := reader.ReadSelectedDBPITRWindow(t.Context(), "db-original", selected, at)
			allowed := scenario == "rolling" || scenario == "retained"
			if allowed != (err == nil) || !allowed && result != nil {
				t.Fatal("PITR selection adopted changed metadata or an expired timestamp", result, err)
			}
			if (scenario == "wrong-selected-source" || scenario == "latest") && reads != 0 {
				t.Fatal("invalid PITR selection contacted the provider", reads)
			}
			if allowed && (!result.Latest.Equal(fresh.Latest) || reads != 1) {
				t.Fatal("PITR used frozen bounds instead of provider evidence", result, reads)
			}
		})
	}
}

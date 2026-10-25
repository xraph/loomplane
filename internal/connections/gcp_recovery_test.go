package connections

import (
	"errors"
	"testing"
)

func TestGCPRecoveryRouteIsOptionalValidatedAndRetained(t *testing.T) {
	credential := gcpKey(t)
	for _, scenario := range []string{"configured", "absent", "invalid", "consumer-override"} {
		t.Run(scenario, func(t *testing.T) {
			settings := map[string]string{"cloud_sql_recovery_network": " projects/recovery-project/global/networks/worker "}
			switch scenario {
			case "absent":
				settings = nil
			case "invalid":
				settings["cloud_sql_recovery_network"] = "default"
			case "consumer-override":
				settings["cloud_sql_recovery_consumer_project"] = "production"
			}
			checked, err := (GCPChecker{}).Parse(credential, settings)
			if scenario == "invalid" || scenario == "consumer-override" {
				if !errors.Is(err, ErrInvalid) {
					t.Fatal("unreviewable recovery route accepted", checked, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := "projects/recovery-project/global/networks/worker"
			if scenario == "absent" {
				want = ""
			}
			if checked.Details["cloud_sql_recovery_network"] != want || checked.Details["project_id"] != "shop" {
				t.Fatal("source account or recovery route discarded", checked.Details)
			}
		})
	}
}

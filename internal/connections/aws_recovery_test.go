package connections

import (
	"errors"
	"maps"
	"testing"
)

func TestAWSRecoveryRouteIsExplicitCompleteAndRetained(t *testing.T) {
	credential := []byte(`{"access_key_id":"AKIATESTTESTTESTTEST","secret_access_key":"test-test-test-test-secret"}`)
	settings := map[string]string{"region": "us-east-1", "rds_recovery_vpc": "vpc-recovery", "rds_recovery_subnet_group": "recovery-private", "rds_recovery_security_group": "sg-copy", "rds_recovery_verifier_group": "sg-worker"}
	checker := AWSChecker{}
	for _, scenario := range []string{"configured", "partial", "shared-source-ingress", "invalid-name", "absent"} {
		t.Run(scenario, func(t *testing.T) {
			in := maps.Clone(settings)
			switch scenario {
			case "partial":
				delete(in, "rds_recovery_verifier_group")
			case "shared-source-ingress":
				in["rds_recovery_verifier_group"] = "sg-copy"
			case "invalid-name":
				in["rds_recovery_subnet_group"] = "public subnet"
			case "absent":
				in = map[string]string{"region": "us-east-1"}
			}
			result, err := checker.Parse(credential, in)
			if scenario != "configured" && scenario != "absent" {
				if !errors.Is(err, ErrInvalid) {
					t.Fatal("unreviewable recovery route accepted", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for key, want := range in {
				if result.Details[key] != want {
					t.Fatal("private recovery route was discarded", key, result.Details[key])
				}
			}
		})
	}
}

package connections

import (
	"encoding/json"
	"testing"
)

func TestConnectionPublishesCurrentAccountIdentity(t *testing.T) {
	for _, cloud := range []string{AWS, GCP} {
		c := Connection{Cloud: cloud, Account: "123456789012", Details: map[string]string{"region": "us-east-1", "project_id": "project-a"}, Status: StatusReady}
		c.normalize()
		read := func() string {
			raw, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Identity string `json:"account_identity"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			return body.Identity
		}
		original := read()
		if original == "" || original != AccountIdentity(c.Cloud, c.Account, c.Details) {
			t.Fatalf("%s connection did not expose its checked scope", cloud)
		}
		c.Details["client_email"] = "rotated@project-a.iam.gserviceaccount.com"
		c.normalize()
		if read() != original {
			t.Fatal("credential rotation changed the account review")
		}
		c.Details["region"], c.Details["project_id"] = "us-west-2", "project-b"
		c.normalize()
		if read() == original {
			t.Fatal("a changed account scope retained a stale public identity")
		}
		c.Details = nil
		c.normalize()
		if read() != "" {
			t.Fatal("an unverifiable account published a review identity")
		}
	}
}

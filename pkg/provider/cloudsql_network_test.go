package provider

import "testing"

func TestCloudSQLApplicationNetworkValid(t *testing.T) {
	valid := CloudSQLApplicationNetwork{ConsumerProject: "app-project", Network: "projects/app-project/global/networks/apps", Subnetwork: "projects/app-project/regions/us-central1/subnetworks/run"}
	for _, tc := range []struct {
		name   string
		change func(*CloudSQLApplicationNetwork)
		region string
		want   bool
	}{
		{"canonical", func(*CloudSQLApplicationNetwork) {}, "us-central1", true},
		{"region mismatch", func(*CloudSQLApplicationNetwork) {}, "us-east1", false},
		{"network host mismatch", func(n *CloudSQLApplicationNetwork) { n.ConsumerProject = "other-project" }, "us-central1", false},
		{"subnet host mismatch", func(n *CloudSQLApplicationNetwork) {
			n.Subnetwork = "projects/other-project/regions/us-central1/subnetworks/run"
		}, "us-central1", false},
		{"missing subnet", func(n *CloudSQLApplicationNetwork) { n.Subnetwork = "" }, "us-central1", false},
		{"foreign URL", func(n *CloudSQLApplicationNetwork) { n.Network = "https://example.com/" + n.Network }, "us-central1", false},
		{"wrong collection", func(n *CloudSQLApplicationNetwork) {
			n.Subnetwork = "projects/app-project/regions/us-central1/forwardingRules/run"
		}, "us-central1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := valid
			tc.change(&n)
			if n.Valid(tc.region) != tc.want {
				t.Fatal("unexpected route validity", n, tc.region)
			}
		})
	}
}

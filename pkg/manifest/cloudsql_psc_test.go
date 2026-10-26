package manifest

import (
	"encoding/json"
	"strings"
	"testing"
)

const pscManifest = `version: 1
resources:
  database:
    kind: postgres
    provider: cloud-sql
    version: '17'
    region: us-central1
    cloud_sql_psc:
      consumer_project: app-project
      network: projects/app-project/global/networks/apps
      forwarding_rule: projects/app-project/regions/us-central1/forwardingRules/database
`

func TestCloudSQLPSCManifestRoundTrip(t *testing.T) {
	m, err := Parse([]byte(pscManifest))
	if err != nil {
		t.Fatal(err)
	}
	endpoint := m.Resources["database"].CloudSQLPSC
	if endpoint == nil || !endpoint.Valid("us-central1") {
		t.Fatal("endpoint contract lost", endpoint)
	}
	doc, err := WithResource([]byte("version: 1\n"), "database", m.Resources["database"])
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(doc)
	if err != nil || again.Resources["database"].CloudSQLPSC == nil || *again.Resources["database"].CloudSQLPSC != *endpoint {
		t.Fatal("endpoint changed during manifest edit", string(doc), err)
	}
	raw, err := json.Marshal(Resource{Kind: "postgres"})
	if err != nil || string(raw) != `{"kind":"postgres"}` {
		t.Fatal("legacy resource encoding changed", string(raw), err)
	}
}

func TestCloudSQLPSCManifestRejectsUnsupportedContracts(t *testing.T) {
	for _, change := range [][2]string{{"kind: postgres", "kind: mysql"}, {"provider: cloud-sql", "provider: rds"}, {"version: '17'", "version: '16'"}, {"region: us-central1", "region: us-east1"}, {"consumer_project: app-project", "consumer_project: other-project"}, {"forwarding_rule:", "forwarding_rule_typo:"}} {
		if _, err := Parse([]byte(strings.Replace(pscManifest, change[0], change[1], 1))); err == nil {
			t.Fatal("invalid PSC manifest accepted", change)
		}
	}
}

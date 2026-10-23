package manifest

import (
	"encoding/json"
	"testing"
)

func TestCatalogOnlyRequiresKnownResourceOnlyConfiguration(t *testing.T) {
	for _, test := range []struct {
		body string
		want bool
	}{
		{`{"version":1,"resources":{"cache":{"kind":"memcached"}}}`, true},
		{`{"version":1,"services":{"api":{}}}`, false},
		{`{"version":1,"gateways":{"edge":{}}}`, false},
		{`{"version":1,"links":{"private":{}}}`, false},
		{`{"version":2}`, false}, {`null`, false},
	} {
		var m *Manifest
		if err := json.Unmarshal([]byte(test.body), &m); err != nil {
			t.Fatal(err)
		}
		if got := m.CatalogOnly(); got != test.want {
			t.Fatalf("wrong plan mode for %s", test.body)
		}
	}
}

package config

import (
	"strings"
	"testing"
)

func TestNodesConfigLoads(t *testing.T) {
	cfg, err := FromSource(mapGetter{"database.url": testDSN, "nodes.advertise_url": "https://a.internal:8780", "nodes.hop_insecure": true, "nodes.hop_ca_file": "/etc/ca.pem"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Nodes.AdvertiseURL != "https://a.internal:8780" || !cfg.Nodes.HopInsecure || cfg.Nodes.HopCAFile != "/etc/ca.pem" {
		t.Fatalf("nodes settings: %+v", cfg.Nodes)
	}
	def, err := FromSource(mapGetter{"database.url": testDSN})
	if err != nil || def.Nodes != (NodesConfig{}) {
		t.Fatalf("defaults: %+v %v", def.Nodes, err)
	}
}

func TestNodesConfigRejectsABadAdvertiseURL(t *testing.T) {
	for _, bad := range []string{"not a url", "ftp://a.internal", "https://u:p@a.internal", "https://a.internal/?x=1"} {
		_, err := FromSource(mapGetter{"database.url": testDSN, "nodes.advertise_url": bad})
		if err == nil || !strings.Contains(err.Error(), "nodes.advertise_url") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

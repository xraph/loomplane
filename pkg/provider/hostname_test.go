package provider

import (
	"strings"
	"testing"
)

func TestHostname(t *testing.T) {
	for in, want := range map[string]string{
		"web-1":                            "web-1",
		"api.internal":                     "api.internal",
		"Digital Ocean Droplet 1 2VPC 4GB": "Digital-Ocean-Droplet-1-2VPC-4GB",
		"  café / prod!! ":                 "caf-prod",
		"-edge-":                           "edge",
		"web..1":                           "web-1",
		"***":                              "loomplane-server",
		"":                                 "loomplane-server",
		strings.Repeat("a b ", 40):         strings.TrimRight(strings.Repeat("a-b-", 16), "-"),
	} {
		got := Hostname(in)
		if got != want {
			t.Errorf("Hostname(%q) = %q, want %q", in, got, want)
		}
		if !ValidHostname(got) || len(got) > 63 && got != in {
			t.Errorf("Hostname(%q) = %q is not a valid hostname", in, got)
		}
	}
}

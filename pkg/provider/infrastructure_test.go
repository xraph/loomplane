package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestInfrastructureSecretsAreNotSerialized(t *testing.T) {
	secret := "bootstrap-or-token-secret"
	for _, v := range []any{InfrastructureCredential{Provider: "digitalocean", AccountID: "team", Token: secret}, ServerSpec{OperationID: "op", UserData: secret}} {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), secret) || strings.Contains(fmt.Sprintf("%v %+v %#v", v, v, v), secret) {
			t.Fatal("transient secret escaped")
		}
	}
}
func TestDNSPriorIncludesIdentityAndEveryManagedValue(t *testing.T) {
	base := DNSRecord{ID: "rec", ZoneID: "zone", Name: "api", Type: "A", Value: "192.0.2.1", TTL: 60, Owner: "loomplane:route"}
	for _, change := range []func(*DNSRecord){
		func(r *DNSRecord) { r.ID = "other" }, func(r *DNSRecord) { r.ZoneID = "other" }, func(r *DNSRecord) { r.Name = "other" }, func(r *DNSRecord) { r.Type = "AAAA" }, func(r *DNSRecord) { r.Value = "192.0.2.2" }, func(r *DNSRecord) { r.TTL = 120 }, func(r *DNSRecord) { r.Owner = "foreign" },
	} {
		current := base
		change(&current)
		if MatchDNSPrior(base, current) == nil {
			t.Fatal("accepted drift")
		}
	}
	if err := MatchDNSPrior(base, base); err != nil {
		t.Fatal(err)
	}
}
func TestDNSValidation(t *testing.T) {
	for _, name := range []string{"../bad", "host/path", "@", "api?query", "bad..name", "UPPER", "a.*"} {
		if ValidateDNSName(name, true) == nil {
			t.Errorf("accepted %q", name)
		}
	}
	for _, name := range []string{"api", "*.test", "_acme-challenge.api", ""} {
		if err := ValidateDNSName(name, true); err != nil {
			t.Errorf("refused %q: %v", name, err)
		}
	}
	for _, kind := range []string{"NS", "MX", "SOA", "SRV"} {
		if ValidateDNSRecord(DNSRecord{Name: "api", Type: kind, Value: "value", TTL: 60}) == nil {
			t.Errorf("unsupported record %s accepted", kind)
		}
	}
}

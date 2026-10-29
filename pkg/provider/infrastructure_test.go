package provider

import (
	"encoding/json"
	"errors"
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

// An apex takes several addresses. Records the caller already wrote on the
// name don't stop the next one while they match.
func TestPlanDNSUpsertAllowsMatchingSiblings(t *testing.T) {
	zone := DNSZone{ID: "zone", Name: "example.com"}
	first := DNSRecord{ID: "r1", ZoneID: "zone", Name: "", Type: "A", Value: "192.0.2.1", TTL: 60, Owner: "loomplane:route:r"}
	next := DNSRecord{ZoneID: "zone", Name: "", Type: "A", Value: "192.0.2.2", TTL: 60, Owner: "loomplane:route:r:1"}
	for _, ownerComments := range []bool{true, false} {
		want := next
		if !ownerComments {
			want.Owner = ""
		}
		existing := first
		if !ownerComments {
			existing.Owner = ""
		}
		if _, _, err := PlanDNSUpsert(DNSUpsertRequest{Zone: zone, Record: want}, []DNSRecord{existing}, ownerComments); !errors.Is(err, ErrInfrastructureDrift) {
			t.Fatalf("owner comments %t: a record nobody claims: %v", ownerComments, err)
		}
		if _, done, err := PlanDNSUpsert(DNSUpsertRequest{Zone: zone, Record: want, Siblings: []DNSRecord{existing}}, []DNSRecord{existing}, ownerComments); err != nil || done {
			t.Fatalf("owner comments %t: a sibling: %v %t", ownerComments, err, done)
		}
		changed := existing
		changed.Value = "198.51.100.9"
		if _, _, err := PlanDNSUpsert(DNSUpsertRequest{Zone: zone, Record: want, Siblings: []DNSRecord{existing}}, []DNSRecord{changed}, ownerComments); !errors.Is(err, ErrInfrastructureDrift) {
			t.Fatalf("owner comments %t: a sibling that changed: %v", ownerComments, err)
		}
	}
}

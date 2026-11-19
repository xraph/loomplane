package digitalocean

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

// rulesOf reads a request body's rules back as the typed form.
func rulesOf(t *testing.T, v any) []firewallRule {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var rules []firewallRule
	if err = json.Unmarshal(data, &rules); err != nil {
		t.Fatal(err)
	}
	return rules
}

func ruleSet(rules []firewallRule) map[string][]string {
	out := map[string][]string{}
	for _, r := range rules {
		targets := r.Sources
		if targets == nil {
			targets = r.Destinations
		}
		out[r.Protocol+" "+r.Ports] = targets.Addresses
	}
	return out
}

// A new firewall lets only Loomplane's addresses reach the SSH port, and
// leaves every other port, in and out, as open as it was without one.
func TestManagementFirewallCreatesItsTagAndRules(t *testing.T) {
	f, c := newFake(t)
	infrastructureAccount(f)
	var tagged, created map[string]any
	f.on("POST /v2/tags", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		tagged = body
		reply(w, 201, map[string]any{"tag": body})
	})
	f.on("GET /v2/firewalls", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"firewalls": []any{map[string]any{"id": "fw-other", "name": "someone-elses"}}})
	})
	f.on("POST /v2/firewalls", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		created = body
		reply(w, 202, map[string]any{"firewall": body})
	})
	err := c.EnsureManagementFirewall(t.Context(), infrastructureCredential(), provider.ManagementFirewall{SSHPort: 2222, Sources: []string{"198.51.100.9/24", "203.0.113.7", "203.0.113.7/32"}})
	if err != nil {
		t.Fatal(err)
	}
	if tagged["name"] != "loomplane-ssh-2222" || created["name"] != "loomplane-ssh-2222" || !slices.Equal(created["tags"].([]any), []any{"loomplane-ssh-2222"}) {
		t.Fatalf("tag %v, firewall %v", tagged, created)
	}
	in, out := ruleSet(rulesOf(t, created["inbound_rules"])), ruleSet(rulesOf(t, created["outbound_rules"]))
	all := []string{"0.0.0.0/0", "::/0"}
	for key, want := range map[string][]string{
		"tcp 2222":       {"198.51.100.0/24", "203.0.113.7/32"},
		"tcp 1-2221":     all,
		"tcp 2223-65535": all,
		"udp 0":          all,
		"icmp 0":         all,
	} {
		if !slices.Equal(in[key], want) {
			t.Fatalf("inbound %s: %v, want %v (all: %v)", key, in[key], want, in)
		}
	}
	if len(in) != 5 || len(out) != 3 || !slices.Equal(out["tcp 0"], all) || !slices.Equal(out["udp 0"], all) || !slices.Equal(out["icmp 0"], all) {
		t.Fatalf("rules: in %v, out %v", in, out)
	}
}

func TestManagementRulesAtTheEdgesOfThePortRange(t *testing.T) {
	for port, want := range map[int][]string{
		22:    {"tcp 22", "tcp 1-21", "tcp 23-65535", "udp 0", "icmp 0"},
		1:     {"tcp 1", "tcp 2-65535", "udp 0", "icmp 0"},
		2:     {"tcp 2", "tcp 1", "tcp 3-65535", "udp 0", "icmp 0"},
		65535: {"tcp 65535", "tcp 1-65534", "udp 0", "icmp 0"},
	} {
		in, _ := managementRules(port, []string{"203.0.113.7/32"})
		var got []string
		for _, r := range in {
			got = append(got, r.Protocol+" "+r.Ports)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("port %d: %v, want %v", port, got, want)
		}
	}
}

// An existing firewall whose rules already match is left alone. One whose
// addresses changed is replaced whole, keeping its Droplets and tags.
func TestManagementFirewallUpdatesOnlyWhatChanged(t *testing.T) {
	f, c := newFake(t)
	infrastructureAccount(f)
	f.on("GET /v2/tags/loomplane-ssh-2222", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"tag": map[string]any{"name": "loomplane-ssh-2222"}})
	})
	inbound, outbound := managementRules(2222, []string{"203.0.113.7/32"})
	// DigitalOcean reads rules back with the default action filled in.
	for i := range inbound {
		inbound[i].Action = "allow"
	}
	current := map[string]any{"id": "fw-1", "name": "loomplane-ssh-2222", "tags": []string{"loomplane-ssh-2222", "keep-me"}, "droplet_ids": []int{5}, "inbound_rules": inbound, "outbound_rules": outbound}
	f.on("GET /v2/firewalls", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"firewalls": []any{current}})
	})
	var put map[string]any
	f.on("PUT /v2/firewalls/fw-1", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		put = body
		reply(w, 200, map[string]any{"firewall": body})
	})
	if err := c.EnsureManagementFirewall(t.Context(), infrastructureCredential(), provider.ManagementFirewall{SSHPort: 2222, Sources: []string{"203.0.113.7"}}); err != nil || put != nil {
		t.Fatalf("unchanged firewall: put %v, %v", put, err)
	}
	if slices.Contains(f.calls, "POST /v2/tags") {
		t.Fatal("an existing tag was created again")
	}
	if err := c.EnsureManagementFirewall(t.Context(), infrastructureCredential(), provider.ManagementFirewall{SSHPort: 2222, Sources: []string{"192.0.2.10"}}); err != nil || put == nil {
		t.Fatalf("changed addresses: put %v, %v", put, err)
	}
	if !slices.Equal(put["tags"].([]any), []any{"loomplane-ssh-2222", "keep-me"}) || !slices.Equal(put["droplet_ids"].([]any), []any{float64(5)}) {
		t.Fatalf("PUT dropped what the firewall had: %v", put)
	}
	if got := ruleSet(rulesOf(t, put["inbound_rules"]))["tcp 2222"]; !slices.Equal(got, []string{"192.0.2.10/32"}) {
		t.Fatalf("new SSH sources: %v", got)
	}
}

func TestManagementFirewallRefusesWhatItCantOwn(t *testing.T) {
	f, c := newFake(t)
	infrastructureAccount(f)
	f.on("GET /v2/tags/loomplane-ssh-2222", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"tag": map[string]any{"name": "loomplane-ssh-2222"}})
	})
	twins := []any{map[string]any{"id": "fw-1", "name": "loomplane-ssh-2222"}, map[string]any{"id": "fw-2", "name": "loomplane-ssh-2222"}}
	f.on("GET /v2/firewalls", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"firewalls": twins})
	})
	good := provider.ManagementFirewall{SSHPort: 2222, Sources: []string{"203.0.113.7"}}
	if err := c.EnsureManagementFirewall(t.Context(), infrastructureCredential(), good); !errors.Is(err, provider.ErrInfrastructureDrift) {
		t.Fatalf("two firewalls with its name: %v", err)
	}

	twins = nil
	f.on("POST /v2/firewalls", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 403, map[string]any{"message": "dop_v1_good"})
	})
	var scope *provider.InfrastructureScopeError
	if err := c.EnsureManagementFirewall(t.Context(), infrastructureCredential(), good); !errors.As(err, &scope) || scope.Permission != "firewall:create" {
		t.Fatalf("a token without firewall access: %v", err)
	}

	before := len(f.calls)
	for _, bad := range []provider.ManagementFirewall{
		{SSHPort: 2222},
		{SSHPort: 2222, Sources: []string{"loomplane.example.com"}},
		{SSHPort: 0, Sources: []string{"203.0.113.7"}},
		{SSHPort: 70000, Sources: []string{"203.0.113.7"}},
	} {
		if err := c.EnsureManagementFirewall(t.Context(), infrastructureCredential(), bad); !errors.Is(err, provider.ErrInfrastructureInvalid) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	if len(f.calls) != before {
		t.Fatal("an invalid firewall reached DigitalOcean")
	}
}

// A create that names the firewall's tag carries it from the start, and
// only that tag shape is accepted.
func TestCreateServerJoinsTheManagementFirewall(t *testing.T) {
	f, c := newFake(t)
	infrastructureAccount(f)
	f.on("GET /v2/droplets", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"droplets": []any{}})
	})
	var created map[string]any
	f.on("POST /v2/droplets", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		created = body
		reply(w, 202, map[string]any{"droplet": map[string]any{"id": 42, "name": body["name"], "tags": body["tags"]}, "links": map[string]any{"actions": []any{map[string]any{"id": 7, "rel": "create"}}}})
	})
	spec := provider.ServerSpec{OperationID: "intent-1", Name: "host", Region: "nyc3", Size: "small", Image: "ubuntu-24-04-x64", FirewallTag: provider.ManagementFirewallTag(2222)}
	if _, err := c.CreateServer(t.Context(), infrastructureCredential(), spec); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(created["tags"].([]any), any("loomplane-ssh-2222")) {
		t.Fatalf("tags: %v", created["tags"])
	}
	spec.FirewallTag = "production"
	if _, err := c.CreateServer(t.Context(), infrastructureCredential(), spec); !errors.Is(err, provider.ErrInfrastructureInvalid) {
		t.Fatalf("another tag: %v", err)
	}
}

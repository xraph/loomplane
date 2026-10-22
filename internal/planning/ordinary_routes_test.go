package planning

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestOrdinaryComputeRetainsRoutedRuntimeBeforeConsumerPropagation(t *testing.T) {
	doc := fmt.Sprintf("version: 1\nservices:\n  api: {source: cmd/api, provider: server, target: %s, public: false}\n", targetA)
	in := Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: infrastructureProviders(t), Connections: infrastructureConnections(t)}
	first := Compute(in)
	a := action(t, first, "service:api")
	raw, err := json.Marshal(a.Service)
	if err != nil {
		t.Fatal(err)
	}
	bound := &Binding{ID: "binding", ExternalID: "source", Subject: ServiceSubject, Name: "api", Provider: "server", ConnectionID: targetA, Config: raw}
	in.Bindings = []*Binding{bound}
	desired := in.Manifest.Services["api"]
	desired.Size = &manifest.Size{CPU: 1, Memory: 1024}
	in.Manifest.Services["api"] = desired
	route := cloudinfra.Route{ID: "route", Status: "ready", Input: cloudinfra.RouteInput{ExternalTTLSeconds: 60, Service: "api"}, Binding: cloudinfra.RouteBinding{BindingID: "binding", InstanceID: "source"}}
	in.Routes = []cloudinfra.Route{route}
	result := Compute(in)
	if !result.Executable {
		t.Fatal(result.Problems)
	}
	changed := action(t, result, "service:api")
	if changed.Op != Replace || len(changed.Routes) != 1 || changed.Routes[0].ID != "route" {
		t.Fatal("ordinary update overwrites routed instance", changed)
	}
	in.Routes[0].Input.ExternalTTLSeconds = 0
	if missing := Compute(in); missing.Executable {
		t.Fatal("missing external TTL permitted replacement planning")
	}
	in.Routes[0].Receipt = &provider.DNSRecord{TTL: 90000}
	if managed := Compute(in); !managed.Executable {
		t.Fatal("trusted provider TTL prevented replacement planning", managed.Problems)
	}
	in.Routes[0].Input.ExternalTTLSeconds = 86401
	if oversized := Compute(in); oversized.Executable {
		t.Fatal("trusted provider receipt bypassed the external TTL input limit")
	}
	consumer := &Action{ID: "service:consumer", Name: "consumer", Op: Keep, Provider: "server", Service: &ServiceConfig{Env: []EnvLink{{From: "api", Subject: ServiceSubject}}}}
	p := planner{services: map[string]*Action{"api": &changed, "consumer": consumer}, bindings: map[string]*Binding{bound.ID: bound}}
	p.readdress()
	if consumer.Op != Update {
		t.Fatal("consumer kept obsolete private endpoint")
	}
}

func TestRoutedReplacementAllowsDistinctPortsOnSameDaemon(t *testing.T) {
	old := ServiceConfig{Public: true, Endpoint: &manifest.Endpoint{HostPort: 8080}, TargetSettings: map[string]string{"docker_id": "daemon"}}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	bound := &Binding{ID: "binding", ExternalID: "source", Provider: "server", Config: raw}
	next := old
	next.Endpoint = &manifest.Endpoint{HostPort: 8081}
	a := Action{Name: "api", Provider: "server", Service: &next}
	route := cloudinfra.Route{Status: "ready", Input: cloudinfra.RouteInput{ExternalTTLSeconds: 60}, Binding: cloudinfra.RouteBinding{BindingID: bound.ID, InstanceID: bound.ExternalID}}
	if err = routedReplacementAllowed(&a, bound, route); err != nil {
		t.Fatal("distinct explicit port denied", err)
	}
}

package planning

import (
	"encoding/json"
	"testing"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/pkg/manifest"

	"github.com/xraph/loomplane/internal/cloudprofiles"
)

func TestChangedSyncTargetCompatibility(t *testing.T) {
	managed := cloudprofiles.Target{ID: "stable", Provider: "vercel", ConnectionID: "old"}
	server := cloudprofiles.Target{ID: "stable", Provider: "server", Runtime: "server", PoolID: "pool"}
	for _, tc := range []struct {
		name                      string
		before                    cloudprofiles.Target
		input                     cloudprofiles.ChangeInput
		runtime, pool, connection string
		invalid                   bool
	}{
		{name: "legacy omission", before: managed, input: cloudprofiles.ChangeInput{ConnectionID: "new"}, runtime: "managed", connection: "new"},
		{name: "managed to pool", before: managed, input: cloudprofiles.ChangeInput{Runtime: "server", Provider: "server", PoolID: "pool"}, runtime: "server", pool: "pool"},
		{name: "pool to managed", before: server, input: cloudprofiles.ChangeInput{Runtime: "managed", Provider: "vercel", ConnectionID: "new"}, runtime: "managed", connection: "new"},
		{name: "pool switch", before: server, input: cloudprofiles.ChangeInput{PoolID: "other"}, runtime: "server", pool: "other"},
		{name: "missing pool", before: managed, input: cloudprofiles.ChangeInput{Runtime: "server", Provider: "server"}, invalid: true},
		{name: "direct connection", before: server, input: cloudprofiles.ChangeInput{ConnectionID: "new"}, invalid: true},
		{name: "managed pool", before: managed, input: cloudprofiles.ChangeInput{PoolID: "pool", ConnectionID: "new"}, invalid: true},
		{name: "legacy direct server stays managed", before: managed, input: cloudprofiles.ChangeInput{Provider: "server", ConnectionID: "new"}, runtime: "managed", connection: "new"},
		{name: "invalid runtime", before: server, input: cloudprofiles.ChangeInput{Runtime: "unknown"}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := changedSyncTarget(tc.before, tc.input)
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid transition accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.ID != "stable" || got.Runtime != tc.runtime || got.PoolID != tc.pool || got.ConnectionID != tc.connection {
				t.Fatalf("unexpected target: %+v", got)
			}
		})
	}
}

func TestRoutedReplacementPhysicalHostIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, oldDaemon, newDaemon, oldServer, newServer string
		blocked                                          bool
	}{
		{name: "distinct direct daemons", oldDaemon: "daemon-a", newDaemon: "daemon-b"},
		{name: "shared daemon with server aliases", oldDaemon: "daemon-a", newDaemon: "daemon-a", oldServer: "server-a", newServer: "server-b", blocked: true},
		{name: "same server", oldServer: "server-a", newServer: "server-a", blocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := ServiceConfig{Public: true, Endpoint: &manifest.Endpoint{HostPort: 8080}, TargetSettings: map[string]string{"docker_id": tc.oldDaemon}, CloudPlacement: &CloudPlacement{ServerID: tc.oldServer}}
			raw, err := json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			b := &Binding{ID: "binding", ExternalID: "instance", Provider: "server", Config: raw}
			next := old
			next.TargetSettings = map[string]string{"docker_id": tc.newDaemon}
			next.CloudPlacement = &CloudPlacement{ServerID: tc.newServer}
			a := &Action{Name: "api", Provider: "server", Service: &next}
			route := cloudinfra.Route{Input: cloudinfra.RouteInput{ExternalTTLSeconds: 60}, Status: "ready", Binding: cloudinfra.RouteBinding{BindingID: b.ID, InstanceID: b.ExternalID}}
			err = routedReplacementAllowed(a, b, route)
			if (err != nil) != tc.blocked {
				t.Fatalf("blocked=%v, error=%v", tc.blocked, err)
			}
		})
	}
}

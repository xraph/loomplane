package planning

import (
	"testing"

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

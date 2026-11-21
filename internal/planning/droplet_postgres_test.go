package planning

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/providers"
)

func TestDropletPostgresPlan(t *testing.T) {
	const account = "team-1"
	const resource = "postgres"
	doc := fmt.Sprintf("version: 1\nservices:\n  api: {source: cmd/api, provider: server, target: %s, public: false, needs: [postgres]}\nresources:\n  postgres: {provider: digitalocean, kind: postgres, region: nyc}\n", targetA)
	for _, tc := range []struct {
		name    string
		hosts   []CloudServer
		problem string
	}{
		{"managed Droplet", []CloudServer{{ID: "droplet", DropletAccount: account}}, ""},
		{"generic SSH", nil, "unreachable-resource"},
		{"unclassified host", []CloudServer{{ID: "droplet"}}, "unreachable-resource"},
		{"ambiguous host", []CloudServer{{ID: "one", DropletAccount: account}, {ID: "two"}}, "unreachable-resource"},
		{"other account", []CloudServer{{ID: "droplet", DropletAccount: "other"}}, "resource-across-accounts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := infrastructureConnections(t)
			options[0].Servers = tc.hosts
			options = append(options, ConnectionOption{Cloud: "digitalocean", ID: "conn_do", Ready: true, Details: map[string]string{"team_uuid": account}})
			in := Input{Manifest: parse(t, doc), Environment: "production", Graph: infrastructureGraph(), Providers: providers.Registry(), Connections: options}
			result := Compute(in)
			if tc.problem != "" {
				if !slices.Contains(findingCodes(result.Problems), tc.problem) || result.Executable {
					t.Fatalf("problems %+v", result.Problems)
				}
				return
			}
			a := action(t, result, "service:api")
			if !result.Executable || !slices.Contains(a.DependsOn, "resource:postgres") || !slices.ContainsFunc(a.Service.Env, func(link EnvLink) bool {
				return link.DropletPostgres && link.Key == "DATABASE_URL" && link.From == resource
			}) {
				t.Fatalf("service %+v problems %+v", a, result.Problems)
			}
			plan := &Plan{OrgID: "org", EnvironmentID: "prod"}
			calls := 0
			var probeErr error
			svc := Service{dropletPostgres: func(_ context.Context, org, target string, db *Binding) error {
				calls++
				if org != "org" || db.EnvironmentID != "prod" || target != targetA || db.Name != resource {
					t.Fatalf("wrong probe %s/%s/%s/%s", org, db.EnvironmentID, target, db.Name)
				}
				return probeErr
			}}
			svc.checkDropletPostgres(t.Context(), plan, result, in.Bindings)
			if calls != 0 || !result.Executable || warning(result, "postgres-check-after-provisioning", "api") == nil {
				t.Fatalf("new database: %+v", result)
			}
			in.Bindings = deployed(t, result)
			for _, b := range in.Bindings {
				b.OrgID, b.EnvironmentID = "org", "prod"
			}
			result = Compute(in)
			svc.checkDropletPostgres(t.Context(), plan, result, in.Bindings)
			if calls != 1 || !result.Executable {
				t.Fatalf("reachable database: calls %d problems %+v", calls, result.Problems)
			}
			probeErr = errors.New("unreachable")
			svc.checkDropletPostgres(t.Context(), plan, result, in.Bindings)
			if calls != 2 || result.Executable || !slices.Contains(findingCodes(result.Problems), "droplet-postgres-unverified") {
				t.Fatalf("unreachable database: %+v", result.Problems)
			}
		})
	}
}

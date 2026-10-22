package deployments

import (
	"context"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

// A direct server action is fenced whatever it deploys, so a catalog
// resource's action arms the fence on the server it targets too. It has
// no service config of its own until Apply makes one.
func TestPreparePoolEffectsArmsTheFenceForResourceActions(t *testing.T) {
	host, _ := sshKey(t)
	_, user := sshKey(t)
	details := map[string]string{
		"key": user, "host": "server.example", "user": "deploy", "docker_socket": "/var/run/docker.sock",
		"host_key": strings.TrimSpace(string(ssh.MarshalAuthorizedKey(host.PublicKey()))), "architecture": "arm64", "docker_id": "daemon",
	}
	reviewed := planning.ReviewedConnectionSettings(connections.Server, details)
	creds := dialCredentials{"conn_server": {ID: "conn_server", OrgID: "org", Cloud: connections.Server, Status: connections.StatusReady, Details: details}}
	refused := errors.New("ssh: handshake refused")
	var dialed *server.Config
	target := &OwnedTarget{Cloud: connections.Server, Credentials: creds, ServerDial: func(_ context.Context, cfg *server.Config) (*server.Client, error) {
		dialed = cfg
		return nil, refused
	}}
	ctx := server.WithEffectFence(t.Context(), server.EffectFence{Operation: "org/plan/app--api", Generation: 1})
	for name, resource := range map[string]*planning.ResourceConfig{
		"component": {Kind: "sample", Provider: "server", Target: "conn_server", TargetSettings: reviewed, Component: &planning.ComponentConfig{
			Kind: "sample", Resource: "app", Component: recipes.ResolvedComponent{Component: recipes.Component{Name: "api", Role: recipes.RoleProcess}},
		}},
		"database":    {Kind: "postgres", Provider: "server", Target: "conn_server", TargetSettings: reviewed},
		"composition": {Kind: "sample", Provider: "server", Target: "conn_server", TargetSettings: reviewed, Composition: &planning.CompositionConfig{Kind: "sample"}},
	} {
		t.Run(name, func(t *testing.T) {
			dialed = nil
			job := &Job{
				Deployment: &Deployment{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", PlanID: "plan", ID: "dep"},
				Connection: creds["conn_server"],
				Action:     planning.Action{ID: "resource:app--api", Name: "app--api", Provider: "server", ConnectionID: "conn_server", Resource: resource},
			}
			if err := target.PreparePoolEffects(ctx, job); !errors.Is(err, refused) {
				t.Fatalf("prepare effects: %v", err)
			}
			if dialed == nil || dialed.Address != "server.example:22" {
				t.Fatalf("the fence wasn't armed on the reviewed server: %+v", dialed)
			}
		})
	}
}

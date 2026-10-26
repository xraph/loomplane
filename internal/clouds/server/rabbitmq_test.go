package server

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/owned"
	native "github.com/xraph/loomplane/internal/clouds/rabbitmq"
)

func TestRabbitMQRestoreRejectsSourceBeforeOpeningTarget(t *testing.T) {
	r := Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
		t.Fatal("source restore opened target")
		return nil, nil
	}}
	if err := r.RestoreDatabase(t.Context(), id.New(id.PrefixInstance), map[string]string{native.LabelEngine: "rabbitmq"}, nil, backupfiles.Object{}); err == nil {
		t.Fatal("live source restore accepted")
	}
}
func TestRabbitMQRecoveryRejectsRuntimeDriftBeforeConnection(t *testing.T) {
	for _, scenario := range []string{"image", "connection", "identity", "public", "published", "egress", "extra-network"} {
		t.Run(scenario, func(t *testing.T) {
			iid := id.New(id.PrefixInstance)
			labels := map[string]string{owned.LabelOrg: "org", owned.LabelProject: "project", owned.LabelEnvironment: "env", owned.LabelConnection: "conn", native.LabelEngine: "rabbitmq", owned.LabelImage: "rabbitmq@sha256:reviewed", LabelRecovery: "copy", owned.LabelPublic: "false"}
			info := containerInfo{}
			info.State.Running = true
			info.Config.Image = labels[owned.LabelImage]
			info.Config.Env = []string{"LOOMPLANE_APP_PASSWORD=abcdefghijklmnopqrstuvwxyz123456"}
			info.Config.Labels = map[string]string{owned.LabelOrg: "org", owned.LabelProject: "project", owned.LabelEnvironment: "env", owned.LabelInstance: iid.String(), owned.LabelConnection: "conn", native.LabelEngine: "rabbitmq", LabelRecovery: "copy", owned.LabelPublic: "false"}
			info.NetworkSettings.Networks = map[string]struct{ IPAddress, NetworkID string }{runtimeNetwork(labels): {"172.18.0.2", "network"}}
			switch scenario {
			case "image":
				info.Config.Image = "other"
			case "connection":
				info.Config.Labels[owned.LabelConnection] = "other"
			case "identity":
				info.Config.Labels[owned.LabelInstance] = "other"
			case "public":
				info.Config.Labels[owned.LabelPublic] = "true"
			case "published":
				info.NetworkSettings.Ports = map[string][]struct{ HostIP, HostPort string }{"5672/tcp": {{"127.0.0.1", "1234"}}}
			case "extra-network":
				info.NetworkSettings.Networks["outside"] = struct{ IPAddress, NetworkID string }{"172.19.0.2", "outside"}
			}
			c := &Client{HTTP: &http.Client{Transport: runtimeTransport(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet {
					t.Fatal("guard made a mutation")
				}
				if req.URL.Path == "/networks/network" {
					return jsonResponse(map[string]any{"Internal": false, "Labels": labels}), nil
				}
				return jsonResponse(info), nil
			})}}
			r := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) { return c, nil }}
			err := r.withRabbitMQ(t.Context(), iid, labels, "", func(native.Database) error { t.Fatal("unsafe recovery reached native command"); return nil })
			if err == nil {
				t.Fatal("unsafe recovery reached RabbitMQ")
			}
			if scenario == "image" && !errors.Is(err, ErrOwned) {
				t.Fatalf("image guard: %v", err)
			}
		})
	}
}

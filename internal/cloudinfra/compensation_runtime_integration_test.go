//go:build integration

package cloudinfra

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
)

type compensationRuntimeStore struct {
	compensationRouteStore
	generation int64
	cancelled  bool
}

func (s *compensationRuntimeStore) BeginServerIngress(context.Context, string, string) (*IngressState, error) {
	s.generation++
	return &IngressState{Generation: s.generation}, nil
}
func (s *compensationRuntimeStore) CancelCompensationOperations(_ context.Context, ops []*RouteOperation) error {
	s.cancelled = true
	for _, op := range ops {
		if op.ID == s.cutover.ID {
			s.cutover.Status = "cancelled"
		}
	}
	return nil
}
func TestIngressCompensationDrainsDetachedMutationBeforeCancellation(t *testing.T) {
	client, host, key := recoveryFixture(t)
	ctx := t.Context()
	oldFence := server.EffectFence{Operation: "org/ingress/server", Generation: 1}
	if err := client.ArmEffectFence(ctx, oldFence); err != nil {
		t.Fatal(err)
	}
	recoveryDocker(t, "exec", host, "docker", "pull", server.IngressImage)
	busy := recoveryDocker(t, "exec", host, "docker", "run", "-d", "--name", "old-effect", server.IngressImage, "sh", "-c", `trap 'echo stopping; sleep 2; exit 0' TERM; while :; do sleep 0.1; done`)
	unrelated := recoveryDocker(t, "exec", host, "docker", "run", "-d", "--name", "unrelated", server.IngressImage, "sleep", "300")
	oldCtx, cancel := context.WithCancel(server.WithEffectFence(ctx, oldFence))
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		response, err := client.Request(oldCtx, http.MethodPost, "/containers/"+busy+"/stop?t=10", nil)
		if err == nil {
			_, err = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		finished <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		output, err := exec.CommandContext(ctx, "docker", "exec", host, "docker", "logs", busy).CombinedOutput()
		if err != nil {
			t.Fatal(err, string(output))
		}
		if strings.Contains(string(output), "stopping") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("mutation never reached Docker")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel() // The detached helper must finish the daemon call before takeover.
	source := Route{ID: "route", OrgID: "org", ProjectID: "project", Input: RouteInput{EnvironmentID: "env", Service: "web"}, Binding: RouteBinding{ServerID: "server", ConnectionID: "runtime", RuntimeID: client.Config.Details["docker_id"], InstanceID: "source"}}
	candidate := source
	candidate.Binding.InstanceID = "candidate"
	st := &compensationRuntimeStore{generation: 1, compensationRouteStore: compensationRouteStore{source: source, destination: candidate}}
	st.prepared = RouteOperation{ID: releasePrepareID("old/action", source.ID), Kind: "prepare", Status: "succeeded", Source: &source, Route: candidate}
	st.cutover = RouteOperation{ID: "pending-release", Kind: "release", HandoffID: st.prepared.ID, Status: "running", Source: &source, Route: candidate}
	conn := &connections.Connection{OrgID: "org", Cloud: "server", Generation: 1, Details: client.Config.Details}
	svc := NewService(Deps{Store: st, Domains: st, Authz: cleanupTestAuth{}, Connections: rotatedRouteCredential{conn: conn, raw: key}})
	if err := svc.PrepareCompensationRoutes(ctx, security.Principal{OrgID: "org", UserID: "manager"}, ReleaseRouteRequest{Key: "old/action", ProjectID: "project", EnvironmentID: "env", Service: "web", Routes: []Route{source}}); err != nil {
		t.Fatal(err)
	}
	if !st.cancelled || recoveryDocker(t, "exec", host, "docker", "inspect", "--format", "{{.State.Running}}", busy) != "false" {
		t.Fatal("cancelled before remote mutation drained")
	}
	response, err := client.Request(server.WithEffectFence(ctx, oldFence), http.MethodPost, "/containers/"+busy+"/start", nil)
	if err == nil {
		defer response.Body.Close()
		if response.StatusCode != http.StatusConflict {
			t.Fatal("old ingress generation still mutates", response.StatusCode)
		}
	}
	if recoveryDocker(t, "exec", host, "docker", "inspect", "--format", "{{.State.Running}}", unrelated) != "true" {
		t.Fatal("unrelated workload changed")
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("detached caller did not return")
	}
}

type compensationPreparationStore struct {
	recoveryIngressStore
	prepared RouteOperation
}

func (s *compensationPreparationStore) RouteOperation(context.Context, string, string) (*RouteOperation, error) {
	return &s.prepared, nil
}

func TestIngressCompensationClearsOnlyAcceptedChallengeAndRecoversLostSave(t *testing.T) {
	c, host, _ := recoveryFixture(t)
	ctx := server.WithEffectFence(t.Context(), server.EffectFence{Operation: "org/ingress/server", Generation: 1})
	previous, err := server.EnsureIngress(ctx, c, "org", "server")
	if err != nil {
		t.Fatal(err)
	}
	container := recoveryDocker(t, "exec", host, "docker", "run", "-d", "--name", "source", "--label", "loomplane.org=org", "--label", "loomplane.project=project", "--label", "loomplane.environment=env", "--label", "loomplane.instance=source", "--label", "loomplane.revision=release", "--label", "loomplane.service=web", server.IngressImage, "sleep", "300")
	backend, err := server.ConnectIngressBackend(ctx, c, "org", "server", server.IngressBackend{OrgID: "org", ProjectID: "project", EnvironmentID: "env", Service: "web", InstanceID: "source", ReleaseID: "release", ContainerID: container, Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	config := server.IngressConfig{OrgID: "org", ServerID: "server", Version: "prepared", Routes: []server.IngressRoute{{ID: "route", Hostname: "source.example.test", Backend: backend, ChallengeAddress: "192.0.2.2"}, {ID: "unrelated", Hostname: "other.example.test", Backend: backend}}}
	base := config
	base.Version = "source"
	base.Routes = slices.Clone(config.Routes)
	base.Routes[0].ChallengeAddress = ""
	previous, err = server.ApplyIngress(ctx, c, previous, base)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := server.ApplyIngress(ctx, c, previous, config)
	if err != nil {
		t.Fatal(err)
	}
	source := Route{ID: "route", OrgID: "org", ProjectID: "project", Input: RouteInput{Hostname: "source.example.test", EnvironmentID: "env", Service: "web", Port: 8080}, Binding: RouteBinding{ServerID: "server", InstanceID: "source", ReleaseID: "release", ContainerID: container, Address: "192.0.2.1"}}
	candidate := source
	candidate.Binding.ServerID = "other-server"
	candidate.Binding.InstanceID = "candidate"
	candidate.Binding.Address = "192.0.2.2"
	st := &compensationPreparationStore{recoveryIngressStore: recoveryIngressStore{failSave: true}, prepared: RouteOperation{ID: "prepared", Kind: "prepare", Route: candidate, Source: &source, SourceTransition: &IngressTransition{Desired: config, Previous: previous, Applied: applied}}}
	op := &RouteOperation{ID: "release", Kind: "release", Route: source, Source: &candidate, HandoffID: "prepared"}
	state := &IngressState{Config: config, Current: applied}
	svc := &Service{}
	newer := config
	newer.Routes = slices.Clone(config.Routes)
	newer.Routes[0].ChallengeAddress = "192.0.2.99"
	newerSnapshot, err := server.ApplyIngress(ctx, c, applied, newer)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.clearInactivePreparation(ctx, st, c, op, &IngressState{Config: newer, Current: newerSnapshot}); !errors.Is(err, ErrConflict) {
		t.Fatal("newer route entry overwritten", err)
	}
	applied, err = server.ApplyIngress(ctx, c, newerSnapshot, config)
	if err != nil {
		t.Fatal(err)
	}
	state.Current = applied
	if err = svc.clearInactivePreparation(ctx, st, c, op, state); err == nil {
		t.Fatal("source challenge was not removed before the injected lost save")
	}
	// The remote write succeeded. A fresh worker recovers its exact saved intention.
	recovered := &IngressState{Config: config, Current: applied}
	retry := st.durable
	if err = svc.clearInactivePreparation(ctx, st, c, &retry, recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.Config.Routes[0].ChallengeAddress != "" || !reflect.DeepEqual(recovered.Config.Routes[1], config.Routes[1]) {
		t.Fatal("candidate challenge or unrelated route changed", recovered.Config)
	}
	actual, err := server.ReadIngress(ctx, c, "org", "server")
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := recovered.Config.JSON()
	if !server.SameIngressConfig(expected, actual.Config) {
		t.Fatal("restored config was not persisted")
	}
}

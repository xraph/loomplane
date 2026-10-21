package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

type pendingPoolBackend struct {
	owned.Backend
	exists, deny bool
	removals     int
}

func (b *pendingPoolBackend) RemovePending(context.Context, id.ID, map[string]string) error {
	if b.deny {
		return errors.New("foreign runtime reference")
	}
	b.removals++
	b.exists = false
	return nil
}
func (b *pendingPoolBackend) Status(context.Context, id.ID, map[string]string) (*cpprovider.InstanceStatus, error) {
	v := "false"
	if b.exists {
		v = "true"
	}
	return &cpprovider.InstanceStatus{Metadata: map[string]string{"exists": v}}, nil
}
func TestPoolCleanupExactOwnershipAndMissingControlRecord(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	ref := "org/env/api/runtime_fixture"
	labels := map[string]string{owned.LabelOrg: "org", owned.LabelProject: "proj", owned.LabelEnvironment: "env", owned.LabelService: "api", owned.LabelConnection: "conn", owned.LabelRuntime: ref}
	instances := &ownedInstances{value: &instance.Instance{Entity: ctrlplane.Entity{ID: iid}, TenantID: "org", ProviderName: "server.conn", Labels: labels}}
	backend := &pendingPoolBackend{exists: true, deny: true}
	secrets := &memSecrets{m: map[string][]byte{ref: []byte("secret")}}
	e := &OwnedTarget{Cloud: connections.Server, Ctrl: CtrlPlane{Providers: cpprovider.NewRegistry(), Instances: instances}, Secrets: secrets, Backend: func(string, string) owned.Backend { return backend }}
	resume, _ := json.Marshal(ownedState{RuntimeRef: ref})
	job := &Job{Deployment: &Deployment{OrgID: "org", ProjectID: "proj", EnvironmentID: "env"}, Plan: &planning.Plan{ID: "plan"}, Connection: &connections.Connection{ID: "conn"}, Action: planning.Action{Name: "api"}, Resume: resume}
	var cp []byte
	save := func(raw []byte) error { cp = append([]byte(nil), raw...); return nil }
	if err := e.CleanupPoolWorkload(t.Context(), job, nil, save); err == nil || backend.removals != 0 || len(cp) == 0 {
		t.Fatalf("unsafe ownership cleanup: %v", err)
	}
	// The exact persisted identity survives a deleted control record; runtime
	// absence is still verified, and unrelated storage never enters this API.
	instances.value = nil
	backend.deny = false
	if err := e.CleanupPoolWorkload(t.Context(), job, cp, save); err != nil {
		t.Fatal(err)
	}
	if backend.exists || backend.removals != 1 || len(secrets.m) != 0 {
		t.Fatal("cleanup did not confirm absence and remove runtime secret")
	}
	var altered poolCleanupCheckpoint
	if err := json.Unmarshal(cp, &altered); err != nil {
		t.Fatal(err)
	}
	altered.Labels[owned.LabelEnvironment] = "foreign"
	foreign, _ := json.Marshal(altered)
	if err := e.CleanupPoolWorkload(t.Context(), job, foreign, save); err == nil {
		t.Fatal("foreign checkpoint accepted")
	}
	if err := e.CleanupPoolWorkload(t.Context(), job, nil, save); err == nil {
		t.Fatal("missing identity treated as proven absence")
	}
}

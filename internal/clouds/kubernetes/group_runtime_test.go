package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/xraph/loomplane/internal/clouds/owned"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func TestPlacementGroupPreparationLeavesIngressUntouched(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	client := fake.NewClientset()
	r := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
		return &Client{Config: &Config{Namespace: "apps", groupIngress: true}, Kube: client}, nil
	}, WaitReady: func(context.Context, *Client, string) error { return nil }}
	labels := map[string]string{owned.LabelOrg: "org", owned.LabelProject: "project", owned.LabelEnvironment: "environment", owned.LabelPublic: "true", owned.LabelManageIngress: "true", owned.LabelHostname: "api.example.com"}
	result, err := r.Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "api", Image: "registry.example/api@sha256:" + strings.Repeat("a", 64), Ports: []cpprovider.PortSpec{{Container: 8080}}}, labels)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Endpoints) != 1 || result.Endpoints[0].Public {
		t.Fatalf("preparation published an endpoint: %+v", result)
	}
	for _, action := range client.Actions() {
		if action.GetResource().Resource == "ingresses" {
			t.Fatalf("preparation touched ingress: %+v", action)
		}
	}
}

type groupAdmissionJournal struct {
	denied bool
	opened int
}

func (g *groupAdmissionJournal) OpenGroupScope(context.Context, ManagedScope) error {
	g.opened++
	if g.denied {
		return ErrManagedAuthority
	}
	return nil
}
func (*groupAdmissionJournal) Issue(context.Context, ManagedScope, ManagedRequest) (string, error) {
	return "", ErrManagedAuthority
}
func (*groupAdmissionJournal) Resolve(context.Context, string, ManagedScope, ManagedOutcome) error {
	return ErrManagedAuthority
}

func TestPlacementGroupIngressModeRequiresTrustedAdmission(t *testing.T) {
	identity, err := domain.NewMemberIdentity("group", 1, "east")
	if err != nil {
		t.Fatal(err)
	}
	scope := ManagedScope{GroupID: "group", MemberID: "east", GroupGeneration: 1, BackendGeneration: 1, GroupOperation: "prepare", Org: "org", Project: "project", Environment: "environment", Service: "api", Plan: "plan", Connection: "connection", Instance: identity.InstanceID, RuntimeRef: identity.RuntimeRef, RuntimeRevision: "config", Namespace: "apps", NamespaceUID: "ns", ClusterCA: strings.Repeat("a", 64), Epoch: 1}
	scope.Release = WorkloadReleaseID(scope.RuntimeRef, scope.RuntimeRevision)
	cfg := &Config{REST: &rest.Config{Host: "https://cluster.invalid"}, Namespace: "apps", Details: map[string]string{"group_ingress": "true", "group_id": "group", "namespace_uid": "ns", "cluster_ca": scope.ClusterCA}}
	ordinary, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !ordinaryIngress(ordinary) {
		t.Fatal("user connection details enabled group ingress mode")
	}
	journal := &groupAdmissionJournal{denied: true}
	if _, err = NewGroupRuntimeClient(t.Context(), cfg, scope, journal); !errors.Is(err, ErrManagedAuthority) {
		t.Fatalf("missing admission accepted: %v", err)
	}
	journal.denied = false
	group, err := NewGroupRuntimeClient(t.Context(), cfg, scope, journal)
	if err != nil {
		t.Fatal(err)
	}
	if ordinaryIngress(group) || cfg.groupIngress || journal.opened != 2 {
		t.Fatal("trusted mode leaked into reusable config")
	}
	for _, change := range []func(*ManagedScope){
		func(s *ManagedScope) { s.LifecycleRunID = "run" },
		func(s *ManagedScope) { s.LifecycleJobName = "job" },
		func(s *ManagedScope) { s.LifecycleDigest = "digest" },
		func(s *ManagedScope) { s.LifecycleSpecDigest = "spec" },
		func(s *ManagedScope) { s.LifecycleSecretDigest = "secret" },
	} {
		mixed := scope
		change(&mixed)
		if managedScopeValid(mixed) {
			t.Fatal("group and lifecycle authority were combined")
		}
	}
	scope.Instance = id.New(id.PrefixInstance).String()
	if _, err = NewGroupRuntimeClient(t.Context(), cfg, scope, journal); !errors.Is(err, ErrManagedAuthority) {
		t.Fatalf("foreign instance admitted: %v", err)
	}
}

func TestPlacementGroupScopePreservesLegacyJSON(t *testing.T) {
	raw, err := json.Marshal(ManagedScope{Org: "org"})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"GroupID", "MemberID", "GroupGeneration", "GroupOperation", "BackendGeneration"} {
		if strings.Contains(string(raw), field) {
			t.Fatalf("legacy authority JSON changed: %s", raw)
		}
	}
}

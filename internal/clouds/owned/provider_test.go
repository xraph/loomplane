package owned

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"
)

type receiptInstances struct {
	value   *instance.Instance
	updates int
	err     error
}

func (s *receiptInstances) Get(context.Context, id.ID) (*instance.Instance, error) {
	return s.value, nil
}
func (s *receiptInstances) Update(_ context.Context, _ id.ID, r instance.UpdateRequest) (*instance.Instance, error) {
	s.updates++
	if s.err != nil {
		return nil, s.err
	}
	s.value.Labels = maps.Clone(r.Labels)
	return s.value, nil
}

type receiptBackend struct {
	Backend
	result                    *cpprovider.ProvisionResult
	status                    *cpprovider.InstanceStatus
	applyLabels, statusLabels map[string]string
	calls                     int
}

func (b *receiptBackend) Apply(_ context.Context, _ id.ID, _ cpprovider.ServiceSpec, labels map[string]string) (*cpprovider.ProvisionResult, error) {
	b.calls++
	b.applyLabels = maps.Clone(labels)
	return b.result, nil
}
func (b *receiptBackend) Status(_ context.Context, _ id.ID, labels map[string]string) (*cpprovider.InstanceStatus, error) {
	b.statusLabels = maps.Clone(labels)
	return b.status, nil
}
func TestDeployPersistsExactReadyRuntimeReceipt(t *testing.T) {
	for _, key := range []string{"server", "kubernetes"} {
		t.Run(key, func(t *testing.T) {
			iid, release := id.New(id.PrefixInstance), id.New(id.PrefixRelease)
			instances := &receiptInstances{value: &instance.Instance{Services: []cpprovider.ServiceSpec{{Name: "main"}}, ServiceRefs: map[string]string{"main": "old-container"}, Labels: map[string]string{LabelOrg: "org", LabelRevision: "initial/action", LabelReceiptServicePrefix + "removed": "stale", "user": "preserved"}}}
			backend := &receiptBackend{result: &cpprovider.ProvisionResult{ProviderRef: "new-runtime", ServiceRefs: map[string]string{"main": "new-container"}}, status: &cpprovider.InstanceStatus{Ready: true}}
			p := &Provider{Key: key, Instances: instances, Backend: backend}
			result, err := p.Deploy(t.Context(), cpprovider.DeployRequest{InstanceID: iid, ReleaseID: release})
			if err != nil || result.Status != "succeeded" || instances.updates != 1 {
				t.Fatalf("deploy: %v %+v", err, result)
			}
			labels := instances.value.Labels
			if labels[LabelRevision] != release.String() || labels[LabelReceiptRelease] != release.String() || labels[LabelReceiptServicePrefix+"main"] != "new-container" || labels[LabelReceiptServicePrefix+"removed"] != "" || labels["user"] != "preserved" {
				t.Fatal("incorrect receipt", labels)
			}
			if backend.statusLabels[LabelRevision] != release.String() || instances.value.ServiceRefs["main"] != "old-container" {
				t.Fatal("receipt must describe the exact new runtime separately from initial refs")
			}
		})
	}
}
func TestDeployNeverPublishesUnreadyOrUnpersistedReceipt(t *testing.T) {
	for _, scenario := range []string{"failed", "missing-reference", "persist-error"} {
		t.Run(scenario, func(t *testing.T) {
			instances := &receiptInstances{value: &instance.Instance{Services: []cpprovider.ServiceSpec{{Name: "main"}}, Labels: map[string]string{LabelRevision: "initial"}}}
			backend := &receiptBackend{result: &cpprovider.ProvisionResult{ServiceRefs: map[string]string{"main": "new"}}, status: &cpprovider.InstanceStatus{Ready: true}}
			switch scenario {
			case "failed":
				backend.status = &cpprovider.InstanceStatus{State: cpprovider.StateFailed}
			case "missing-reference":
				backend.result.ServiceRefs = nil
			case "persist-error":
				instances.err = errors.New("write failed")
			}
			p := &Provider{Instances: instances, Backend: backend}
			if result, err := p.Deploy(t.Context(), cpprovider.DeployRequest{InstanceID: id.New(id.PrefixInstance), ReleaseID: id.New(id.PrefixRelease)}); err == nil || result != nil {
				t.Fatal("incomplete deployment succeeded")
			}
			if instances.value.Labels[LabelRevision] != "initial" || instances.value.Labels[LabelReceiptRelease] != "" {
				t.Fatal("unready receipt published")
			}
		})
	}
}
func TestProvisionPreservesInitialRuntimeContract(t *testing.T) {
	labels := map[string]string{LabelOrg: "org", LabelRevision: "deployment/action"}
	backend := &receiptBackend{result: &cpprovider.ProvisionResult{ServiceRefs: map[string]string{"main": "created-container"}}}
	p := &Provider{Backend: backend}
	result, err := p.Provision(t.Context(), cpprovider.ProvisionRequest{TenantID: "org", InstanceID: id.New(id.PrefixInstance), Services: []cpprovider.ServiceSpec{{Name: "main"}}, Labels: labels})
	if err != nil || result.ServiceRefs["main"] != "created-container" || backend.applyLabels[LabelRevision] != "deployment/action" || labels[LabelReceiptRelease] != "" {
		t.Fatalf("initial contract: %+v %v", result, err)
	}
}

// Read-only stores are valid for observation, but cannot start a deployment that
// would leave its exact runtime identity unrecorded.
type readOnlyReceiptInstances struct{ Instances }

func TestDeployRequiresReceiptPersistenceBeforeEffects(t *testing.T) {
	backend := &receiptBackend{}
	p := &Provider{Instances: readOnlyReceiptInstances{}, Backend: backend}
	if _, err := p.Deploy(t.Context(), cpprovider.DeployRequest{}); err == nil || backend.calls != 0 {
		t.Fatal("deployment started without receipt persistence", err)
	}
}

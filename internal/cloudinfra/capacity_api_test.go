package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/security"
)

// capacityAPIClouds resolves every Cloud in the caller's organization.
type capacityAPIClouds struct{}

func (capacityAPIClouds) GetProfile(_ context.Context, org, _, id string) (*cloudprofiles.Profile, error) {
	return &cloudprofiles.Profile{ID: id, OrgID: org}, nil
}

// capacityAPIStore answers the usage reads the API adds on top of the runtime store.
type capacityAPIStore struct {
	capacityRuntimeStore
	usage     *CapacityUsage
	frontends []FrontendUsage
}

func (s *capacityAPIStore) GetCapacityUsage(context.Context, string, string) (*CapacityUsage, error) {
	v := *s.usage
	return &v, nil
}
func (s *capacityAPIStore) FrontendUsage(context.Context, string, string) ([]FrontendUsage, error) {
	return s.frontends, nil
}

func TestCapacityPolicyGetReturnsEmptyLists(t *testing.T) {
	svc := NewService(Deps{Store: &capacityRuntimeStore{}, Clouds: capacityAPIClouds{}, Authz: cleanupTestAuth{}})
	policy, err := svc.GetCapacityPolicy(t.Context(), security.Principal{OrgID: "org", UserID: "manager"}, "cloud")
	if err != nil {
		t.Fatal(err)
	}
	if policy.Pools == nil || len(policy.Pools) != 0 || policy.FrontendLimits == nil || len(policy.FrontendLimits) != 0 {
		t.Fatalf("lists: %+v", policy)
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"pools":[]`) || !strings.Contains(string(raw), `"frontend_limits":[]`) {
		t.Fatalf("json: %s", raw)
	}
}

func TestCapacityUsageNeedsCloudManager(t *testing.T) {
	limit := int64(3)
	st := &capacityAPIStore{usage: &CapacityUsage{}, frontends: []FrontendUsage{{TargetID: "vercel", Maximum: &limit, Counted: 2, Active: 1, Pending: 1}}}
	svc := NewService(Deps{Store: st, Clouds: capacityAPIClouds{}, Authz: overviewAuth{
		"admin": {{security.OrganizationCloudManage, "org"}},
		"dev":   {{security.InfrastructureRead, "shop"}, {security.InfrastructureModify, "shop"}},
	}})
	if _, err := svc.GetCapacityUsage(t.Context(), overviewDev, "cloud"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("project-only principal read Cloud usage: %v", err)
	}
	if _, err := svc.ListExpansions(t.Context(), overviewDev, "cloud"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("project-only principal listed expansions: %v", err)
	}
	usage, err := svc.GetCapacityUsage(t.Context(), overviewAdmin, "cloud")
	if err != nil {
		t.Fatal(err)
	}
	if usage.GPUs == nil || len(usage.Frontends) != 1 || usage.Frontends[0].Maximum == nil || *usage.Frontends[0].Maximum != 3 {
		t.Fatalf("usage: %+v", usage)
	}
	raw, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"gpus":[]`, `"frontends":[{"target_id":"vercel","maximum":3,"counted":2,"active":1,"pending":1,"retained":0}]`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("json lacks %s: %s", want, raw)
		}
	}
	// An unlimited target says so with null, never a zero maximum.
	st.frontends = []FrontendUsage{{TargetID: "vercel"}}
	if usage, err = svc.GetCapacityUsage(t.Context(), overviewAdmin, "cloud"); err != nil {
		t.Fatal(err)
	}
	if raw, err = json.Marshal(usage.Frontends); err != nil || !strings.Contains(string(raw), `"maximum":null`) {
		t.Fatalf("unlimited: %s %v", raw, err)
	}
	// No managed targets still returns a list.
	st.frontends = nil
	if usage, err = svc.GetCapacityUsage(t.Context(), overviewAdmin, "cloud"); err != nil || usage.Frontends == nil {
		t.Fatalf("empty frontends: %+v %v", usage, err)
	}
}

func TestCapacityPolicyEnablementNamesCeiling(t *testing.T) {
	for i, name := range capacityCeilingNames {
		p := capacityPolicyFixture()
		p.Pools = nil
		switch i {
		case 0:
			p.Limits.Servers = 0
		case 1:
			p.Limits.CPUMillis = 0
		case 2:
			p.Limits.MemoryBytes = 0
		case 3:
			p.Limits.StorageBytes = 0
		}
		if err := validateCapacityPolicyShape(p); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "positive "+name+" ceiling") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestApproveExpansionRefusesReadOnlyKey(t *testing.T) {
	svc := NewService(Deps{Store: &capacityRuntimeStore{}, Clouds: capacityAPIClouds{}, Authz: cleanupTestAuth{}})
	if _, err := svc.ApproveExpansion(t.Context(), security.Principal{OrgID: "org", UserID: "manager", KeyID: "key", ReadOnly: true}, "cloud", "exp", 1); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("read-only key: %v", err)
	}
}

func TestCapacityUsageUnknownIsNullAndListsAreEmpty(t *testing.T) {
	// No accounts or records yet, and an H100 count no provider could read.
	st := &capacityAPIStore{usage: &CapacityUsage{GPUs: []GPUUsage{{Kind: "nvidia-h100-80gb", Pending: 1}}}}
	svc := NewService(Deps{Store: st, Clouds: capacityAPIClouds{}, Authz: overviewAuth{"admin": {{security.OrganizationCloudManage, "org"}}}})
	usage, err := svc.GetCapacityUsage(t.Context(), overviewAdmin, "cloud")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"cloud_id":"cloud"`,
		`"accounts":[]`, `"records":[]`, `"frontends":[]`,
		`"observed_storage_bytes":null`,
		`"gpus":[{"kind":"nvidia-h100-80gb","provisioned":null,"pending":1,"reserved":0,"limit":null}]`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("json lacks %s: %s", want, raw)
		}
	}
}

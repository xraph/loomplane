package cloudinfra

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/platformcloud"
)

// fakePlatformHosts stands in for Loomplane Cloud's control plane. It
// records every host it is asked to open and opens none.
type fakePlatformHosts struct {
	opened []string
	apex   string
}

func (f *fakePlatformHosts) Apex(context.Context) (string, error) { return f.apex, nil }

func (f *fakePlatformHosts) Attach(_ context.Context, orgID, projectID, envID, resource, name string) (*fc.Client, fc.Scope, *platformcloud.Placement, error) {
	f.opened = append(f.opened, orgID+"/"+projectID+"/"+envID+"/"+resource+"/"+name)
	return nil, fc.Scope{}, nil, errors.New("the fake opens no host")
}

// platformCloudBinding is a service Loomplane Cloud runs, reached through its
// host's ingress.
func platformCloudBinding() RouteBinding {
	return RouteBinding{Backend: RouteBackendFirecracker, BindingID: "binding_web", ConnectionID: fc.PlatformConnection, InstanceID: "inst_web",
		ServerID: "firecracker:" + fc.PlatformConnection, Address: "203.0.113.9"}
}

// A custom domain reaches a Loomplane Cloud host only from a zone the
// route's own organization has verified.
func TestPlatformRouteNeedsVerifiedZone(t *testing.T) {
	for name, change := range map[string]func(z *Zone){
		"unverified": func(z *Zone) {
			z.VerifiedAt, z.ProofName, z.ProofValue, z.ProofExpiresAt = time.Time{}, "", "", time.Time{}
		},
		"another org's zone": func(z *Zone) { z.OrgID = "org_other" },
	} {
		t.Run(name, func(t *testing.T) {
			svc, st, _, dns := kubernetesRouteFixture(t)
			hosts := &fakePlatformHosts{}
			svc.PlatformRoutes = hosts
			st.binding = platformCloudBinding()
			st.op.Route.Binding = st.binding
			change(st.zone)
			if err := svc.ReconcileRoute(t.Context(), "org", st.op.ID, nil); err != nil && !errors.Is(err, ErrPending) {
				t.Fatal(err)
			}
			if st.op.Status != "failed" || st.op.Message != "Add and verify this domain first." {
				t.Fatalf("operation %s: %q", st.op.Status, st.op.Message)
			}
			if len(hosts.opened) != 0 || dns.writes != 0 || st.published != 0 {
				t.Fatalf("opened %v, %d DNS writes, %d published", hosts.opened, dns.writes, st.published)
			}
		})
	}
	// From a verified zone the route goes on to the host the service runs on.
	svc, st, _, _ := kubernetesRouteFixture(t)
	hosts := &fakePlatformHosts{}
	svc.PlatformRoutes = hosts
	st.binding = platformCloudBinding()
	st.op.Route.Binding = st.binding
	_ = svc.ReconcileRoute(t.Context(), "org", st.op.ID, nil)
	if len(hosts.opened) != 1 || hosts.opened[0] != "org/shop/prod/inst_web/octo--gateway" {
		t.Fatalf("opened %v, operation %s: %q", hosts.opened, st.op.Status, st.op.Message)
	}
}

// A name under the platform apex is refused even from a verified zone that
// holds it: Loomplane Cloud names services there itself.
func TestPlatformRouteRefusesTheApex(t *testing.T) {
	svc, st, _, dns := kubernetesRouteFixture(t)
	hosts := &fakePlatformHosts{apex: "example.com"} // the fixture's verified zone
	svc.PlatformRoutes = hosts
	st.binding = platformCloudBinding()
	st.op.Route.Binding = st.binding
	_ = svc.ReconcileRoute(t.Context(), "org", st.op.ID, nil)
	if st.op.Status != "failed" || !strings.Contains(st.op.Message, "Loomplane Cloud's own domain") || len(hosts.opened) != 0 || dns.writes != 0 {
		t.Fatalf("operation %s: %q, opened %v", st.op.Status, st.op.Message, hosts.opened)
	}
}

// A route to or from a Loomplane Cloud service is never handed off.
func TestPlatformCloudRouteRefusesHandoff(t *testing.T) {
	for name, platformSide := range map[string]bool{"from Loomplane Cloud": true, "to Loomplane Cloud": false} {
		t.Run(name, func(t *testing.T) {
			svc, st, _, _ := kubernetesRouteFixture(t)
			route := st.op.Route
			route.Status = "ready"
			if platformSide {
				route.Binding = platformCloudBinding()
			} else {
				route.Binding = RouteBinding{Backend: RouteBackendServer, BindingID: "binding_gw", ServerID: "srv_1", Address: "203.0.113.10"}
				st.binding = platformCloudBinding()
			}
			st.route = &route
			_, err := svc.PrepareRouteHandoff(t.Context(), kubeRouteManager, "shop", route.ID, HandoffInput{Revision: route.Revision})
			if !errors.Is(err, errPlatformCloudHandoff) {
				t.Fatalf("handoff: %v", err)
			}
		})
	}
}

package deployments

import (
	"context"
	"time"

	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/owned"
	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/platformcloud"
)

// generatedRouteID is the host route that serves a service's generated
// hostname. One service has one such name.
const generatedRouteID = "generated-hostname"

// publish gives a public service that listens on a port its generated
// hostname, once it is up on the placement p: the host's ingress routes the
// name to the VM, and platformcloud points the name's record at the host.
// The VM itself publishes no port, so the host's ingress is the only way in.
// A service that isn't public, or has no port, loses any name it had.
func (t *PlatformTarget) publish(ctx context.Context, c *fc.Client, scope fc.Scope, p *platformcloud.Placement, labels map[string]string, spec fc.Spec, r fc.Record) (*cpprovider.Endpoint, error) {
	route := func(hostname string, remove bool) error {
		q := fc.Request{Scope: scope, RuntimeIdentity: c.Details["runtime_identity"],
			Authority: fc.Authority{Generation: r.Generation, Operation: r.Operation, Expires: time.Now().Add(2 * time.Minute)},
			Route:     &fc.RouteSpec{ID: generatedRouteID, Hostname: hostname, Port: spec.Port, Remove: remove}}
		_, err := c.Route(ctx, q)
		return err
	}
	if labels[owned.LabelPublic] != "true" || spec.Port == 0 {
		hostname, err := t.Platform.Unpublish(ctx, p.OrgID, p.EnvironmentID, p.Resource)
		if err != nil || hostname == "" {
			return nil, err
		}
		return nil, route(hostname, true)
	}
	hostname, err := t.Platform.Publish(ctx, p.OrgID, p.ID)
	if err != nil || hostname == "" {
		return nil, err
	}
	if checkErr := route(hostname, false); checkErr != nil {
		return nil, checkErr
	}
	return &cpprovider.Endpoint{ServiceName: labels[owned.LabelService], URL: "https://" + hostname, Port: spec.Port, Protocol: "https", Public: true}, nil
}

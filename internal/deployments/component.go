package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

var _ planning.TargetDialer = (*OwnedTarget)(nil)

// TargetDial opens connections from a target's network, for checking an
// external endpoint the way the target will reach it. A server dials over
// its SSH connection, opened and checked the way a deployment opens it, so
// the host key, Docker identity and reviewed settings must still match;
// closing the io.Closer ends that connection. A cluster's checks dial
// from Loomplane: nothing runs inside the cluster to dial from.
func (e *OwnedTarget) TargetDial(ctx context.Context, orgID, connectionID string) (catalogconnect.DialFunc, io.Closer, error) {
	if e.Credentials == nil {
		return nil, nil, ErrNoCredential
	}
	c, _, err := e.Credentials.Credential(ctx, orgID, connectionID)
	if err != nil {
		return nil, nil, err
	}
	if c == nil || c.ID != connectionID || c.OrgID != orgID || c.Status != connections.StatusReady {
		return nil, nil, errors.New("the target connection isn't ready")
	}
	if c.Cloud != e.Cloud {
		return nil, nil, fmt.Errorf("%s is a %s connection, not a %s one", c.Name, c.Cloud, e.Cloud)
	}
	switch e.Cloud {
	case connections.Server:
		settings, _ := json.Marshal(planning.ReviewedConnectionSettings(c.Cloud, c.Details)) //nolint:errcheck // string maps always encode
		labels := map[string]string{owned.LabelOrg: orgID, owned.LabelConnection: connectionID, owned.LabelSettings: string(settings)}
		client, err := e.serverSession(orgID, connectionID)(ctx, labels)
		if err != nil {
			return nil, nil, err
		}
		return client.DialContext, client, nil
	case connections.Kubernetes:
		return (&net.Dialer{}).DialContext, noClose{}, nil
	}
	return nil, nil, fmt.Errorf("endpoints can't be checked from a %s target", e.Cloud)
}

// noClose is an io.Closer with nothing to close.
type noClose struct{}

func (noClose) Close() error { return nil }

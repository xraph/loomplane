package workloadidentity

import (
	"context"
	"errors"
	"net"
	"net/url"
	"sync"
	"time"

	bundlev1 "github.com/spiffe/spire-api-sdk/proto/spire/api/server/bundle/v1"
	entryv1 "github.com/spiffe/spire-api-sdk/proto/spire/api/server/entry/v1"
	spiretypes "github.com/spiffe/spire-api-sdk/proto/spire/api/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	domain "github.com/xraph/loomplane/pkg/federation"
)

const adminTimeout = 10 * time.Second

// Admin limits Loomplane operations on a trusted local SPIRE administration
// socket to one organization. Socket permissions remain the authority boundary.
type Admin struct {
	connection  *grpc.ClientConn
	issuer      *SPIREIssuer
	entries     entryv1.EntryClient
	bundles     bundlev1.BundleClient
	org, domain string
	closeOnce   sync.Once
	closeErr    error
}

// OpenAdmin verifies the server trust domain and administrative access before
// returning. It never falls back to TCP or ambient proxy configuration.
func OpenAdmin(ctx context.Context, address, trustDomain, org string) (*Admin, error) {
	if !domain.ValidUnixSocketAddress(address) {
		return nil, ErrIdentity
	}
	if _, err := ControlURIForOrganization(trustDomain, org); err != nil {
		return nil, err
	}
	socket, err := url.Parse(address)
	if err != nil {
		return nil, ErrIdentity
	}
	connection, err := grpc.NewClient("passthrough:///loomplane-spire-admin",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithNoProxy(),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket.Path)
		}),
	)
	if err != nil {
		return nil, err
	}
	issuer, err := NewSPIREIssuer(connection, trustDomain)
	if err != nil {
		return nil, errors.Join(err, connection.Close())
	}
	a := &Admin{connection: connection, issuer: issuer, entries: entryv1.NewEntryClient(connection), bundles: bundlev1.NewBundleClient(connection), org: org, domain: trustDomain}
	if err := a.Health(ctx); err != nil {
		return nil, errors.Join(err, a.Close())
	}
	return a, nil
}

func (a *Admin) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	bundle, err := a.bundles.GetBundle(ctx, &bundlev1.GetBundleRequest{})
	if err != nil {
		return err
	}
	if bundle == nil || bundle.TrustDomain != a.domain || len(bundle.X509Authorities) == 0 {
		return ErrIdentity
	}
	// GetBundle alone is public. The exact-ID count checks administrative access
	// without enumerating registrations belonging to other organizations.
	_, err = a.entries.CountEntries(ctx, &entryv1.CountEntriesRequest{Filter: &entryv1.CountEntriesRequest_Filter{BySpiffeId: &spiretypes.SPIFFEID{TrustDomain: a.domain, Path: controlPathPrefix + a.org}}})
	return err
}

func (a *Admin) Register(ctx context.Context, binding Binding) (*spiretypes.Entry, error) {
	if binding.Agent.OrgID != a.org || binding.Destination.OrgID != a.org {
		return nil, ErrIdentity
	}
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	if err := a.Health(ctx); err != nil {
		return nil, err
	}
	return a.issuer.Register(ctx, binding)
}

// Revoke inspects the entry before deleting it. Missing entries are idempotent;
// foreign organizations, control identities and privileged entries are denied.
func (a *Admin) Revoke(ctx context.Context, entryID string) error {
	if !validEntryID(entryID) {
		return ErrIdentity
	}
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	if err := a.Health(ctx); err != nil {
		return err
	}
	entry, err := a.entries.GetEntry(ctx, &entryv1.GetEntryRequest{Id: entryID})
	if status.Code(err) == codes.NotFound {
		return nil
	}
	if err != nil {
		return err
	}
	if entry == nil || entry.Id != entryID || entry.SpiffeId == nil || entry.SpiffeId.TrustDomain != a.domain || entry.Admin || entry.Downstream {
		return ErrIdentity
	}
	if entry.ParentId == nil || entry.ParentId.TrustDomain != a.domain || entry.ParentId.Path == "/spire/server" {
		return ErrIdentity
	}
	agent, err := ParseAgentURI("spiffe://" + entry.SpiffeId.TrustDomain + entry.SpiffeId.Path)
	if err != nil || agent.OrgID != a.org {
		return ErrIdentity
	}
	return a.issuer.Revoke(ctx, entryID)
}

func (a *Admin) Close() error {
	a.closeOnce.Do(func() { a.closeErr = a.connection.Close() })
	return a.closeErr
}

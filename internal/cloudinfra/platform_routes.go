package cloudinfra

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/provider"
)

// PlatformDomains adds a hostname to an app a provider's platform runs and
// routes itself, such as DigitalOcean App Platform. The platform serves the
// certificate; Loomplane writes the DNS records it asks for.
type PlatformDomains interface {
	// Records are the records hostname needs to reach the app. apex is a
	// hostname at its DNS zone's apex, where a CNAME can't sit.
	Records(ctx context.Context, cred provider.InfrastructureCredential, b RouteBinding, hostname string, apex bool) ([]PlatformRecord, error)
	// Attach adds hostname to the app's spec as of reviewedCommit, which may
	// start a deployment.
	Attach(ctx context.Context, cred provider.InfrastructureCredential, b RouteBinding, hostname, reviewedCommit string) error
	Status(ctx context.Context, cred provider.InfrastructureCredential, b RouteBinding, hostname string) (PlatformDomainStatus, error)
	// Detach takes hostname out of the app's spec, as Attach adds it.
	Detach(ctx context.Context, cred provider.InfrastructureCredential, b RouteBinding, hostname, reviewedCommit string) error
}

// PlatformRecord is one DNS record a platform hostname needs. Its name is
// always the hostname.
type PlatformRecord struct {
	Type, Value string
}

// PlatformDomainStatus is what the platform reports for a hostname. CertState
// is issuing, ready or failed, as in RouteVerification.
type PlatformDomainStatus struct {
	Attached    bool
	CertState   string
	CertExpires time.Time
	Message     string
}

// RouteCost is what a route adds to the bill, with the same JSON as
// models.Cost's fields of these names.
type RouteCost struct {
	MonthlyUSD string `json:"monthly_usd"`
	Estimate   bool   `json:"estimate"`
	Source     string `json:"source"`
}

// Refusals for routes App Platform can't serve. The private and port ones
// come from ResolveRouteBinding, so they hold on every recheck.
const (
	platformPrivateService = "This service is private on App Platform, so no hostname can reach it. Make it public in the manifest and deploy it first."
	platformBlockedPaths   = "Blocked paths aren't supported on App Platform yet."
	platformWildcards      = "Wildcard hostnames aren't supported on App Platform yet."
)

// platformProviders are the bindings whose services route on their
// provider's app platform.
var platformProviders = []string{connections.DigitalOcean}

// routeRecordOwner is the owner token of a route's ith record, so records on
// one name don't share a token. The first keeps the token routes always had.
func routeRecordOwner(routeID string, i int) string {
	if i == 0 {
		return "loomplane:route:" + routeID
	}
	return "loomplane:route:" + routeID + ":" + strconv.Itoa(i)
}

// desiredRecords reads an operation from before record lists as its one record.
func (op *RouteOperation) desiredRecords() []provider.DNSRecord {
	if op.DesiredRecords != nil {
		return op.DesiredRecords
	}
	if op.DesiredRecord != (provider.DNSRecord{}) {
		return []provider.DNSRecord{op.DesiredRecord}
	}
	return nil
}

func (op *RouteOperation) receipts() []provider.DNSRecord {
	return routeReceipts(op.Receipts, op.Receipt)
}

func (r Route) receipts() []provider.DNSRecord {
	return routeReceipts(r.Receipts, r.Receipt)
}

func routeReceipts(list []provider.DNSRecord, single *provider.DNSRecord) []provider.DNSRecord {
	if list != nil {
		return list
	}
	if single != nil {
		return []provider.DNSRecord{*single}
	}
	return nil
}

// checkPlatformService refuses a service App Platform gives no public
// address, or a port other than the one it routes. public and port are the
// binding config's, as text.
func checkPlatformService(public, port string, want int) error {
	if public != "true" {
		return fmt.Errorf("%w: %s", ErrInvalid, platformPrivateService)
	}
	if port != strconv.Itoa(want) {
		return fmt.Errorf("%w: %s", ErrInvalid, "App Platform routes a hostname only to this service's public port, "+port+".")
	}
	return nil
}

// checkPlatformRoute refuses what App Platform domains can't express.
func checkPlatformRoute(in RouteInput) error {
	if len(in.BlockedPathPrefixes) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalid, platformBlockedPaths)
	}
	return nil
}

// platformRoute opens the app platform of the route's binding while its
// connection is still the one the route was accepted on. Cleanup keeps
// working after the credential rotates.
func (s *Service) platformRoute(ctx context.Context, route Route, cleanup bool) (PlatformDomains, provider.InfrastructureCredential, error) {
	conn, raw, err := s.Connections.Credential(ctx, route.OrgID, route.Binding.ConnectionID)
	if err != nil {
		return nil, provider.InfrastructureCredential{}, err
	}
	if conn.OrgID != route.OrgID || conn.Cloud != route.Binding.Provider || (!cleanup && conn.Generation != route.Binding.ConnectionGeneration) {
		return nil, provider.InfrastructureCredential{}, ErrConflict
	}
	cred, err := Credential(conn, raw)
	if err != nil {
		return nil, cred, err
	}
	adapter, err := s.Adapters.Platform(route.Binding.Provider)
	return adapter, cred, err
}

// platformDNSRecords are the records the platform asks for, named as
// routeDNSRecord names a route's record. One at the apex takes addresses.
func (s *Service) platformDNSRecords(ctx context.Context, zone *Zone, route Route) ([]provider.DNSRecord, error) {
	adapter, cred, err := s.platformRoute(ctx, route, false)
	if err != nil {
		return nil, err
	}
	hostname := route.Input.Hostname
	apex := zone.Name
	if zone.Mode == "managed" {
		apex = zone.ProviderZone.Name
	}
	wanted, err := adapter.Records(ctx, cred, route.Binding, hostname, hostname == apex)
	if err != nil {
		return nil, err
	}
	if len(wanted) == 0 {
		return nil, fmt.Errorf("%w: the platform gave no records for %s", provider.ErrInfrastructureInvalid, hostname)
	}
	out := make([]provider.DNSRecord, 0, len(wanted))
	for i, want := range wanted {
		record, recordErr := routeDNSRecord(zone, hostname, want.Value, route.ID)
		if recordErr != nil {
			return nil, recordErr
		}
		// The value decides the type, so a record whose value doesn't fit
		// its type isn't written as something else.
		if record.Type != want.Type {
			return nil, fmt.Errorf("%w: the platform gave a %s record valued %s", provider.ErrInfrastructureInvalid, want.Type, want.Value)
		}
		if record.Owner != "" {
			record.Owner = routeRecordOwner(route.ID, i)
		}
		out = append(out, record)
	}
	return out, nil
}

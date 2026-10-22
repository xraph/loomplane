package cloudinfra

import (
	"context"
	"fmt"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// ChallengeCredentialInput attaches a zone's DNS challenge connection. An
// empty ConnectionID detaches it, once no wildcard route uses the zone.
type ChallengeCredentialInput struct {
	ConnectionID string `json:"connection_id"`
	Revision     int64  `json:"revision"`
}

// challengeScoper is a DNS adapter that can show what a token reaches.
// Cloudflare's does.
type challengeScoper interface {
	ChallengeScope(ctx context.Context, credential provider.InfrastructureCredential, zoneID string) (cloudflare.ChallengeScope, error)
}

// SetZoneChallengeCredential attaches the connection whose token answers
// the zone's DNS-01 challenges. Servers that serve a wildcard keep that
// token on disk, so it must reach this one zone and nothing else: a
// Cloudflare connection on the zone's account that no other zone uses,
// whose token lists only this zone, can write its DNS and can't touch the
// registrar.
func (s *Service) SetZoneChallengeCredential(ctx context.Context, p security.Principal, zoneID string, in ChallengeCredentialInput) (*Zone, error) {
	if err := s.domainScope(ctx, p, "", true); err != nil {
		return nil, err
	}
	st, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	zone, err := st.Zone(ctx, p.OrgID, zoneID)
	if err != nil {
		return nil, err
	}
	if zone.Revision != in.Revision {
		return nil, ErrConflict
	}
	if in.ConnectionID == "" {
		routes, routesErr := st.OrgRoutes(ctx, p.OrgID)
		if routesErr != nil {
			return nil, routesErr
		}
		for _, route := range routes {
			if route.Input.ZoneID == zone.ID && route.Status != "removed" && isWildcard(route.Input.Hostname) {
				return nil, fmt.Errorf("%w: remove the wildcard routes on %s first", ErrConflict, zone.Name)
			}
		}
	} else if checkErr := s.checkChallengeCredential(ctx, st, zone, in.ConnectionID); checkErr != nil {
		return nil, checkErr
	}
	zone.ChallengeConnectionID = in.ConnectionID
	if checkErr := st.SaveZone(ctx, zone); checkErr != nil {
		return nil, checkErr
	}
	s.domainAudit(ctx, p, "domain.zone.challenge_credential", zone.ID, "")
	return zone, nil
}

func (s *Service) checkChallengeCredential(ctx context.Context, st DomainStore, zone *Zone, connectionID string) error {
	if zone.Mode != "managed" || zone.Provider != connections.Cloudflare || zone.ProviderZone.ID == "" {
		return fmt.Errorf("%w: wildcard certificates need a Cloudflare zone", ErrInvalid)
	}
	if connectionID == zone.ConnectionID {
		return fmt.Errorf("%w: use a second connection, not the zone's own", ErrInvalid)
	}
	zones, err := st.Zones(ctx, zone.OrgID)
	if err != nil {
		return err
	}
	for _, other := range zones {
		if other.ID != zone.ID && (other.ConnectionID == connectionID || other.ChallengeConnectionID == connectionID) {
			return fmt.Errorf("%w: that connection already serves %s", ErrInvalid, other.Name)
		}
	}
	conn, raw, err := s.Connections.Credential(ctx, zone.OrgID, connectionID)
	if err != nil {
		return err
	}
	_, err = s.checkChallengeScope(ctx, zone, conn, raw)
	return err
}

// checkChallengeScope holds a challenge connection to its rules: Cloudflare
// on the zone's account, a token that lists only this zone, can write its
// DNS and is refused by the registrar. It runs on attach and again before
// every write of the token to a server, since a replaced connection or a
// token widened at Cloudflare keeps its connection ID.
func (s *Service) checkChallengeScope(ctx context.Context, zone *Zone, conn *connections.Connection, raw []byte) (provider.InfrastructureCredential, error) {
	if conn.OrgID != zone.OrgID || conn.Cloud != connections.Cloudflare {
		return provider.InfrastructureCredential{}, fmt.Errorf("%w: the challenge credential must be a Cloudflare connection", ErrInvalid)
	}
	credential, err := Credential(conn, raw)
	if err != nil {
		return provider.InfrastructureCredential{}, err
	}
	if credential.AccountID != zone.AccountID {
		return provider.InfrastructureCredential{}, provider.ErrInfrastructureIdentity
	}
	adapter, err := s.Adapters.DNS(zone.Provider)
	if err != nil {
		return provider.InfrastructureCredential{}, err
	}
	scoper, ok := adapter.(challengeScoper)
	if !ok {
		return provider.InfrastructureCredential{}, &provider.UnsupportedCapabilityError{Provider: zone.Provider, Capability: "dns challenge"}
	}
	scope, err := scoper.ChallengeScope(ctx, credential, zone.ProviderZone.ID)
	if err != nil {
		return provider.InfrastructureCredential{}, err
	}
	only := &provider.InfrastructureScopeError{Provider: zone.Provider, Permission: "a token scoped to " + zone.Name + " only"}
	if len(scope.ZoneIDs) != 1 || scope.ZoneIDs[0] != zone.ProviderZone.ID {
		return provider.InfrastructureCredential{}, only
	}
	if scope.Permissions.DNSWrite != cloudflare.Granted {
		return provider.InfrastructureCredential{}, &provider.InfrastructureScopeError{Provider: zone.Provider, Permission: "DNS Write on " + zone.Name}
	}
	if scope.Permissions.RegistrarRead != cloudflare.Missing || scope.Permissions.RegistrarWrite != cloudflare.Missing {
		return provider.InfrastructureCredential{}, only
	}
	return credential, nil
}

// zoneChallengeToken reads the zone's DNS challenge token from the vault.
// It goes only to a proxy's token file, never to a log or an error.
func (s *Service) zoneChallengeToken(ctx context.Context, zone *Zone) ([]byte, error) {
	token, _, err := s.zoneChallengeCredential(ctx, zone)
	return token, err
}

// zoneChallengeCredential is the token with the connection generation it
// came from, once the connection is ready and passes checkChallengeScope.
func (s *Service) zoneChallengeCredential(ctx context.Context, zone *Zone) ([]byte, int64, error) {
	if zone.ChallengeConnectionID == "" || !zone.Wildcards() {
		return nil, 0, ErrWildcardUnsupported
	}
	conn, raw, err := s.Connections.Credential(ctx, zone.OrgID, zone.ChallengeConnectionID)
	if err != nil {
		return nil, 0, err
	}
	if conn.Status != connections.StatusReady {
		return nil, 0, fmt.Errorf("%w: the DNS challenge connection for %s isn't ready", ErrConflict, zone.Name)
	}
	credential, err := s.checkChallengeScope(ctx, zone, conn, raw)
	if err != nil {
		return nil, 0, err
	}
	return []byte(strings.TrimSpace(credential.Token)), conn.Generation, nil
}

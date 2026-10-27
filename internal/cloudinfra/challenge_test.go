package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/provider"
)

// challengeConnections maps connection IDs to Cloudflare connections, each
// with the token tokenFor gives it.
type challengeConnections map[string]*connections.Connection

func (c challengeConnections) Credential(_ context.Context, org, id string) (*connections.Connection, []byte, error) {
	conn, ok := c[id]
	if !ok || conn.OrgID != org {
		return nil, nil, connections.ErrNotFound
	}
	v := *conn
	if token, malformed := malformedTokens[id]; malformed {
		return &v, []byte(token), nil
	}
	return &v, []byte(tokenFor(id, v.Generation)), nil
}

// tokenFor is a connection's token in the format Caddy's Cloudflare module
// accepts: the connection ID with a -token suffix, and -g<generation> once
// the connection has been replaced.
func tokenFor(id string, generation int64) string {
	token := "cfat_" + id + "-token"
	if generation > 1 {
		token += fmt.Sprintf("-g%d", generation)
	}
	return token + "_" + strings.Repeat("0", 32)
}

// malformedTokens are connections whose token Caddy's module would refuse.
var malformedTokens = map[string]string{"cf-short": "too-short"}

// challengeDNS answers like Cloudflare for acme.dev. scopes holds what each
// token reaches; probed records the zone each DNS Write probe aimed at.
type challengeDNS struct {
	provider.DNSAdapter
	scopes map[string]cloudflare.ChallengeScope
	probed []string
}

func (d *challengeDNS) GetZone(_ context.Context, c provider.InfrastructureCredential, _ string) (provider.DNSZone, error) {
	return provider.DNSZone{ID: "cfzone1", Name: "acme.dev", AccountID: c.AccountID, Verified: true}, nil
}
func (d *challengeDNS) ListRecords(context.Context, provider.InfrastructureCredential, provider.DNSZone) ([]provider.DNSRecord, error) {
	return nil, nil
}
func (d *challengeDNS) ChallengeScope(_ context.Context, c provider.InfrastructureCredential, zoneID string) (cloudflare.ChallengeScope, error) {
	d.probed = append(d.probed, zoneID)
	scope, ok := d.scopes[c.Token]
	if !ok {
		return cloudflare.ChallengeScope{}, cloudflare.ErrRejected
	}
	return scope, nil
}

func zoneScoped(zones ...string) cloudflare.ChallengeScope {
	return cloudflare.ChallengeScope{ZoneIDs: zones, Permissions: cloudflare.Permissions{ZoneRead: cloudflare.Granted, DNSWrite: cloudflare.Granted, RegistrarRead: cloudflare.Missing, RegistrarWrite: cloudflare.Missing}}
}

func challengeFixture(t *testing.T) (*Service, *overviewStore, *challengeDNS) {
	t.Helper()
	svc, st := overviewFixture()
	acme := st.zones[0]
	acme.ConnectionID, acme.ConnectionGeneration, acme.AccountID = "cf-main", 1, "acct"
	acme.ProviderZone = provider.DNSZone{ID: "cfzone1", Name: "acme.dev", AccountID: "acct", Verified: true}
	st.zones = append(st.zones, &Zone{ID: "zone_other", OrgID: "org", Name: "other.dev", Mode: "managed", Provider: "cloudflare", ConnectionID: "cf-other", AccountID: "acct", ChallengeConnectionID: "cf-taken", Revision: 1})
	cf := func(id, account string) *connections.Connection {
		return &connections.Connection{ID: id, OrgID: "org", Cloud: connections.Cloudflare, Generation: 1, Status: "ready", Details: map[string]string{"account_id": account}}
	}
	conns := challengeConnections{}
	for _, id := range []string{"cf-main", "cf-dns", "cf-wide", "cf-registrar", "cf-readonly", "cf-taken", "cf-other", "cf-short"} {
		conns[id] = cf(id, "acct")
	}
	conns["cf-elsewhere"] = cf("cf-elsewhere", "another-acct")
	conns["do"] = &connections.Connection{ID: "do", OrgID: "org", Cloud: connections.DigitalOcean, Details: map[string]string{"account_uuid": "acct"}}
	registrar := zoneScoped("cfzone1")
	registrar.Permissions.RegistrarRead = cloudflare.Granted
	readonly := zoneScoped("cfzone1")
	readonly.Permissions.DNSWrite = cloudflare.Missing
	dns := &challengeDNS{scopes: map[string]cloudflare.ChallengeScope{
		tokenFor("cf-dns", 1): zoneScoped("cfzone1"), tokenFor("cf-wide", 1): zoneScoped("cfzone1", "cfzone2"), tokenFor("cf-registrar", 1): registrar,
		tokenFor("cf-readonly", 1): readonly, tokenFor("cf-taken", 1): zoneScoped("cfzone1"), tokenFor("cf-elsewhere", 1): zoneScoped("cfzone1"),
		"too-short": zoneScoped("cfzone1"),
	}}
	adapters := NewAdapters()
	if err := adapters.RegisterDNS("cloudflare", dns); err != nil {
		t.Fatal(err)
	}
	svc.Connections, svc.Adapters, svc.Resolver = conns, adapters, proofResolver{[]string{"proof"}}
	return svc, st, dns
}

func TestChallengeCredentialMustBeZoneScoped(t *testing.T) {
	svc, st, dns := challengeFixture(t)
	set := func(connection string) (*Zone, error) {
		return svc.SetZoneChallengeCredential(t.Context(), overviewAdmin, "zone", ChallengeCredentialInput{ConnectionID: connection, Revision: 1})
	}
	if _, err := set("cf-wide"); !errors.Is(err, provider.ErrInfrastructureScope) || !strings.Contains(err.Error(), "a token scoped to acme.dev only") {
		t.Fatal("a token listing two zones", err)
	}
	if _, err := set("cf-registrar"); !errors.Is(err, provider.ErrInfrastructureScope) || !strings.Contains(err.Error(), "a token scoped to acme.dev only") {
		t.Fatal("a token with registrar access", err)
	}
	if _, err := set("cf-readonly"); !errors.Is(err, provider.ErrInfrastructureScope) || !strings.Contains(err.Error(), "DNS Write on acme.dev") {
		t.Fatal("a token without DNS Write", err)
	}
	if _, err := set("cf-main"); !errors.Is(err, ErrInvalid) {
		t.Fatal("the zone's own connection", err)
	}
	if _, err := set("cf-taken"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "other.dev") {
		t.Fatal("a connection another zone uses", err)
	}
	if _, err := set("cf-other"); !errors.Is(err, ErrInvalid) {
		t.Fatal("another zone's own connection", err)
	}
	if _, err := set("cf-elsewhere"); !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatal("a connection on another account", err)
	}
	if _, err := set("do"); !errors.Is(err, ErrInvalid) {
		t.Fatal("a connection that isn't Cloudflare", err)
	}
	// A registrar probe Cloudflare didn't answer is no scope refusal.
	dns.scopes[tokenFor("cf-readonly", 1)] = func() cloudflare.ChallengeScope {
		scope := zoneScoped("cfzone1")
		scope.Permissions.RegistrarWrite = cloudflare.Unchecked
		return scope
	}()
	if _, err := set("cf-readonly"); !errors.Is(err, ErrConflict) || errors.Is(err, provider.ErrInfrastructureScope) {
		t.Fatal("an unanswered registrar probe", err)
	}
	probes := len(dns.probed)
	if _, err := set("cf-short"); !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "too-short") || len(dns.probed) != probes {
		t.Fatal("a token Caddy's Cloudflare module would refuse", err, dns.probed)
	}
	if _, err := svc.SetZoneChallengeCredential(t.Context(), overviewDev, "zone", ChallengeCredentialInput{ConnectionID: "cf-dns", Revision: 1}); err == nil {
		t.Fatal("a project member attached a credential")
	}
	if _, err := svc.SetZoneChallengeCredential(t.Context(), overviewAdmin, "zone", ChallengeCredentialInput{ConnectionID: "cf-dns", Revision: 7}); !errors.Is(err, ErrConflict) {
		t.Fatal("a stale revision", err)
	}
	if st.zones[0].ChallengeConnectionID != "" || st.zones[0].Revision != 1 {
		t.Fatalf("a refused credential was saved: %+v", st.zones[0])
	}
	zone, err := set("cf-dns")
	if err != nil {
		t.Fatal(err)
	}
	if zone.ChallengeConnectionID != "cf-dns" || zone.Revision != 2 || !zone.Wildcards() || st.zones[0].ChallengeConnectionID != "cf-dns" {
		t.Fatalf("saved: %+v", zone)
	}
	for _, probed := range dns.probed {
		if probed != "cfzone1" {
			t.Fatal("the DNS Write probe aimed at another zone", dns.probed)
		}
	}
	token, err := svc.zoneChallengeToken(t.Context(), zone)
	if err != nil || string(token) != tokenFor("cf-dns", 1) {
		t.Fatal("challenge token", string(token), err)
	}
	short := *zone
	short.ChallengeConnectionID = "cf-short"
	if _, err = svc.zoneChallengeToken(t.Context(), &short); !errors.Is(err, ErrInvalid) {
		t.Fatal("a malformed token on its way to a write", err)
	}
	moved := *zone
	moved.AccountID = "another-acct"
	if _, err = svc.zoneChallengeToken(t.Context(), &moved); !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatal("a token read for a zone on another account", err)
	}

	// Detaching waits until no wildcard route uses the zone.
	st.routes = append(st.routes, &Route{ID: "route_w", OrgID: "org", ProjectID: "shop", Status: "ready", Input: RouteInput{ZoneID: "zone", Hostname: "*.acme.dev"}})
	if _, err = svc.SetZoneChallengeCredential(t.Context(), overviewAdmin, "zone", ChallengeCredentialInput{Revision: 2}); !errors.Is(err, ErrConflict) {
		t.Fatal("detached under a wildcard route", err)
	}
	st.routes[len(st.routes)-1].Status = "removed"
	if zone, err = svc.SetZoneChallengeCredential(t.Context(), overviewAdmin, "zone", ChallengeCredentialInput{Revision: 2}); err != nil || zone.ChallengeConnectionID != "" || zone.Wildcards() {
		t.Fatal("detach", zone, err)
	}
	if _, err = svc.zoneChallengeToken(t.Context(), zone); !errors.Is(err, ErrWildcardUnsupported) {
		t.Fatal("a token for a zone without a credential", err)
	}
}

const restartNotice = "Every HTTPS hostname on web-1 is unavailable for a few seconds while Loomplane restarts its proxy with DNS challenge support. Certificates and settings stay in their volumes."

func TestWildcardPreviewNoticesProxyRestart(t *testing.T) {
	svc, base, _ := challengeFixture(t)
	base.zones[0].ChallengeConnectionID = "cf-dns"
	st := &gatewayStore{overviewStore: base, clouds: map[string]string{"octo--gateway": "cloud_1"}}
	svc.Domains = st
	pinned, legacy, checked := true, true, 0
	svc.ingressImagePinned = func() bool { return pinned }
	svc.ingressNeedsUpgrade = func(_ context.Context, route Route) (bool, error) {
		checked++
		if route.Binding.ServerID != "srv_1" {
			t.Fatal("checked another server", route.Binding)
		}
		return legacy, nil
	}
	in := RouteInput{ZoneID: "zone", EnvironmentID: "prod", Service: "octo--gateway", Hostname: "*.acme.dev", Port: 8080}
	op, err := svc.PreviewRoute(t.Context(), overviewDev, "shop", in)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(op.Notices, []string{restartNotice}) || op.Route.Input.Hostname != "*.acme.dev" || op.DesiredRecord.Name != "*" {
		t.Fatalf("server on the stock proxy: %q %+v", op.Notices, op.DesiredRecord)
	}
	legacy = false
	if op, err = svc.PreviewRoute(t.Context(), overviewDev, "shop", in); err != nil || op.Notices != nil {
		t.Fatalf("server on the DNS-capable proxy: %q %v", op.Notices, err)
	}
	exact := in
	exact.Hostname = "api.acme.dev"
	before := checked
	if op, err = svc.PreviewRoute(t.Context(), overviewDev, "shop", exact); err != nil || op.Notices != nil || checked != before {
		t.Fatalf("an exact hostname: %q %v", op.Notices, err)
	}

	// Until the DNS-capable image is published, no server takes a wildcard.
	pinned, legacy = false, true
	stored := len(st.previewed)
	_, err = svc.PreviewRoute(t.Context(), overviewDev, "shop", in)
	if !errors.Is(err, ErrWildcardUnsupported) || err.Error() != "Wildcard routes on servers need Loomplane's DNS-capable proxy image, which isn't published yet." {
		t.Fatal("unpublished proxy image", err)
	}
	if len(st.previewed) != stored {
		t.Fatal("a refused preview was stored")
	}
	if _, err = svc.PreviewRoute(t.Context(), overviewDev, "shop", exact); err != nil {
		t.Fatal("exact hostnames need no new image", err)
	}
}

func TestZoneTokenIsRecheckedBeforeEveryWrite(t *testing.T) {
	svc, st, dns := challengeFixture(t)
	zone := st.zones[0]
	zone.ChallengeConnectionID = "cf-dns"
	conns := svc.Connections.(challengeConnections)
	var written []string
	svc.writeDNSToken = func(_ context.Context, _ *server.Client, _, serverID, zoneID string, token []byte) error {
		written = append(written, serverID+"/"+zoneID+"="+string(token))
		return nil
	}
	route := Route{ID: "route_w", OrgID: "org", Input: RouteInput{ZoneID: "zone", Hostname: "*.acme.dev"}, Binding: RouteBinding{ServerID: "srv_1"}}
	state := &IngressState{}
	sync := func(always bool) (bool, error) {
		return svc.syncZoneToken(t.Context(), nil, route, state, zone, always)
	}
	refused := func(name string, wrote bool, err error, want string) {
		t.Helper()
		var notice *routeNoticeError
		if wrote || !errors.As(err, &notice) || !strings.Contains(notice.message, "didn't put its token on the server") || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: wrote %v, %v", name, wrote, err)
		}
	}
	if wrote, err := sync(true); !wrote || err != nil || !slices.Equal(written, []string{"srv_1/zone=" + tokenFor("cf-dns", 1)}) || state.DNSTokens["zone"] != 1 {
		t.Fatal("first write", wrote, err, written)
	}
	probes := len(dns.probed)
	if wrote, err := sync(false); wrote || err != nil || len(written) != 1 || len(dns.probed) != probes {
		t.Fatal("a check rewrote or reprobed an unchanged token", wrote, err)
	}

	// Rotated, still scoped to the zone: the next check puts it on the server.
	conns["cf-dns"].Generation = 2
	dns.scopes[tokenFor("cf-dns", 2)] = zoneScoped("cfzone1")
	if wrote, err := sync(false); !wrote || err != nil || written[len(written)-1] != "srv_1/zone="+tokenFor("cf-dns", 2) || state.DNSTokens["zone"] != 2 {
		t.Fatal("rotated token", wrote, err, written)
	}

	// Replaced with an account-wide token under the same connection ID.
	conns["cf-dns"].Generation = 3
	dns.scopes[tokenFor("cf-dns", 3)] = zoneScoped("cfzone1", "cfzone2")
	wrote, err := sync(false)
	refused("replaced with a wider token", wrote, err, "a token scoped to acme.dev only")
	wrote, err = sync(true)
	refused("replaced with a wider token, on apply", wrote, err, "a token scoped to acme.dev only")

	// Widened at Cloudflare without any change in Loomplane.
	conns["cf-dns"].Generation = 2
	widened := zoneScoped("cfzone1")
	widened.Permissions.RegistrarWrite = cloudflare.Granted
	dns.scopes[tokenFor("cf-dns", 2)] = widened
	wrote, err = sync(true)
	refused("widened at Cloudflare", wrote, err, "a token scoped to acme.dev only")

	dns.scopes[tokenFor("cf-dns", 2)] = zoneScoped("cfzone1")
	conns["cf-dns"].Status = "incomplete"
	wrote, err = sync(true)
	refused("connection not ready", wrote, err, "isn't ready")
	if len(written) != 2 || state.DNSTokens["zone"] != 2 {
		t.Fatal("a refused token reached the server", written, state.DNSTokens)
	}
}

// A Cloudflare token reaches whole zones, so a Loomplane zone inside a larger
// Cloudflare zone takes no challenge token: it would edit the parent's apex
// and every sibling zone.
func TestWildcardsNeedTheirOwnCloudflareZone(t *testing.T) {
	svc, base, dns := challengeFixture(t)
	shop := base.zones[0]
	shop.Name, shop.ProofName = "shop.acme.dev", "_loomplane.shop.acme.dev"
	if _, err := svc.SetZoneChallengeCredential(t.Context(), overviewAdmin, "zone", ChallengeCredentialInput{ConnectionID: "cf-dns", Revision: 1}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "shop.acme.dev to be its own Cloudflare zone, not part of acme.dev") {
		t.Fatal("a challenge token for a zone inside acme.dev", err)
	}
	if shop.ChallengeConnectionID != "" || shop.Revision != 1 || len(dns.probed) != 0 {
		t.Fatalf("a refused credential was probed or saved: %+v %v", shop, dns.probed)
	}

	// One attached before the rule still serves no wildcard and is never read.
	shop.ChallengeConnectionID = "cf-dns"
	if shop.Wildcards() {
		t.Fatal("wildcards on a zone inside acme.dev")
	}
	if _, err := ValidateRouteHostname(shop, "*.shop.acme.dev"); !errors.Is(err, ErrWildcardUnsupported) {
		t.Fatal("wildcard hostname on a zone inside acme.dev", err)
	}
	if token, err := svc.zoneChallengeToken(t.Context(), shop); !errors.Is(err, ErrWildcardUnsupported) || token != nil {
		t.Fatal("challenge token read for a zone inside acme.dev", err)
	}
	st := &gatewayStore{overviewStore: base, clouds: map[string]string{"octo--gateway": "cloud_1"}}
	svc.Domains = st
	svc.ingressImagePinned = func() bool { return true }
	svc.ingressNeedsUpgrade = func(context.Context, Route) (bool, error) { return false, nil }
	in := RouteInput{ZoneID: "zone", EnvironmentID: "prod", Service: "octo--gateway", Hostname: "*.shop.acme.dev", Port: 8080}
	if _, err := svc.PreviewRoute(t.Context(), overviewDev, "shop", in); !errors.Is(err, ErrWildcardUnsupported) || len(st.previewed) != 0 {
		t.Fatal("wildcard preview on a zone inside acme.dev", err)
	}
	in.Hostname = "api.shop.acme.dev"
	if _, err := svc.PreviewRoute(t.Context(), overviewDev, "shop", in); err != nil {
		t.Fatal("an exact hostname on the same zone", err)
	}
}

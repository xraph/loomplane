package cloudinfra

import (
	"context"
	"errors"
	"net"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// fakeRouteCluster stands in for the cluster a Kubernetes route reaches.
type fakeRouteCluster struct {
	log           *[]string
	prerequisites kubernetes.RoutePrerequisites
	status        kubernetes.RouteIngressStatus
	applied       []kubernetes.RouteIngress
	issuerClass   string
	directory     string
	dnsTokens     []string
}

func (c *fakeRouteCluster) CheckRoutePrerequisites(context.Context) (kubernetes.RoutePrerequisites, error) {
	*c.log = append(*c.log, "prerequisites")
	return c.prerequisites, nil
}
func (c *fakeRouteCluster) EnsureHTTPIssuer(_ context.Context, ingressClass, directory string) (string, error) {
	*c.log = append(*c.log, "issuer")
	c.issuerClass, c.directory = ingressClass, directory
	return kubernetes.HTTPIssuerName, nil
}
func (c *fakeRouteCluster) EnsureDNSIssuer(_ context.Context, zoneID, zoneName string, token []byte, directory string) (string, error) {
	*c.log = append(*c.log, "dns issuer "+zoneID+" "+zoneName)
	c.dnsTokens, c.directory = append(c.dnsTokens, string(token)), directory
	return kubernetes.DNSIssuerName(zoneID), nil
}
func (c *fakeRouteCluster) RemoveUnusedDNSIssuer(_ context.Context, zoneID string) error {
	*c.log = append(*c.log, "remove dns issuer "+zoneID)
	return nil
}
func (c *fakeRouteCluster) ApplyRouteIngress(_ context.Context, in kubernetes.RouteIngress) error {
	*c.log = append(*c.log, "ingress")
	c.applied = append(c.applied, in)
	return nil
}
func (c *fakeRouteCluster) RouteIngressStatus(_ context.Context, routeID, service string) (kubernetes.RouteIngressStatus, error) {
	*c.log = append(*c.log, "status "+routeID+" "+service)
	return c.status, nil
}
func (c *fakeRouteCluster) DeleteRouteIngress(_ context.Context, routeID string) error {
	*c.log = append(*c.log, "delete ingress "+routeID)
	return nil
}

// orderedDNS logs record deletion next to the cluster's calls.
type orderedDNS struct {
	*routeDNSAdapter
	log *[]string
}

func (a orderedDNS) DeleteRecord(ctx context.Context, c provider.InfrastructureCredential, req provider.DNSDeleteRequest) error {
	*a.log = append(*a.log, "delete dns "+req.Expected.ID)
	return a.routeDNSAdapter.DeleteRecord(ctx, c, req)
}

// kubeRouteStore keeps one route and its current operation in memory.
type kubeRouteStore struct {
	Store
	DomainStore
	zone      *Zone
	binding   RouteBinding
	route     *Route
	op        RouteOperation
	locks     []string
	published int
}

func (s *kubeRouteStore) WithOperationLock(_ context.Context, key string, run func() error) error {
	s.locks = append(s.locks, key)
	return run()
}
func (s *kubeRouteStore) Zone(context.Context, string, string) (*Zone, error) {
	zone := *s.zone
	return &zone, nil
}
func (s *kubeRouteStore) RouteBindingCloud(context.Context, string, string, string, string) (string, error) {
	return "", nil
}
func (s *kubeRouteStore) ResolveRouteBinding(context.Context, string, string, RouteInput) (RouteBinding, error) {
	return s.binding, nil
}
func (s *kubeRouteStore) PreviewRoute(_ context.Context, op *RouteOperation) error {
	s.op = *op
	return nil
}
func (s *kubeRouteStore) AcceptRoute(_ context.Context, op *RouteOperation) error {
	s.op = *op
	return nil
}
func (s *kubeRouteStore) RouteOperation(context.Context, string, string) (*RouteOperation, error) {
	op := s.op
	return &op, nil
}
func (s *kubeRouteStore) SaveRouteOperation(_ context.Context, op *RouteOperation) error {
	s.op = *op
	return nil
}
func (s *kubeRouteStore) BeginIngress(_ context.Context, op *RouteOperation) (*IngressState, error) {
	s.op = *op
	return &IngressState{}, nil
}
func (s *kubeRouteStore) PublishRoute(_ context.Context, op *RouteOperation) error {
	op.Route.Receipt, op.Route.OperationID = op.Receipt, op.ID
	op.Route.Revision++
	route := op.Route
	s.route, s.op, s.published = &route, *op, s.published+1
	return nil
}
func (s *kubeRouteStore) Route(context.Context, string, string, string) (*Route, error) {
	if s.route == nil {
		return nil, ErrNotFound
	}
	route := *s.route
	return &route, nil
}

type addressResolver struct{ addresses []string }

func (addressResolver) LookupTXT(context.Context, string) ([]string, error) {
	return []string{"proof"}, nil
}
func (r addressResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	out := make([]net.IPAddr, 0, len(r.addresses))
	for _, address := range r.addresses {
		out = append(out, net.IPAddr{IP: net.ParseIP(address)})
	}
	return out, nil
}

var kubeRouteManager = security.Principal{OrgID: "org", UserID: "manager"}

func kubernetesBinding(address string) RouteBinding {
	return RouteBinding{Backend: RouteBackendKubernetes, BindingID: "binding_gw", ConnectionID: "conn_k8s", ConnectionGeneration: 1, InstanceID: "inst_gw",
		RuntimeID: "namespace-uid", Namespace: "shop", ServiceName: "lp-inst-gw", IngressClass: "nginx", Address: address}
}

// kubernetesRouteFixture holds an accepted apply operation for api.example.com.
// The binding declares 203.0.113.10 while the Ingress reports 198.51.100.7.
func kubernetesRouteFixture(t *testing.T) (*Service, *kubeRouteStore, *fakeRouteCluster, *routeDNSAdapter) {
	t.Helper()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	log := []string{}
	dns := &routeDNSAdapter{ownership: true}
	registry := NewAdapters()
	if err := registry.RegisterDNS("digitalocean", orderedDNS{dns, &log}); err != nil {
		t.Fatal(err)
	}
	zone := &Zone{ID: "zone", OrgID: "org", Name: "example.com", Mode: "managed", ConnectionID: "dns", ConnectionGeneration: 1, Provider: "digitalocean", AccountID: "account",
		RecordOwnership: true, ProviderZone: provider.DNSZone{ID: "zone", Name: "example.com"}, ProjectIDs: []string{"shop"}, VerifiedAt: now,
		ProofName: "_loomplane.example.com", ProofValue: "proof", ProofExpiresAt: now.Add(24 * time.Hour)}
	binding := kubernetesBinding("203.0.113.10")
	input := RouteInput{ZoneID: "zone", EnvironmentID: "prod", Service: "octo--gateway", Hostname: "api.example.com", Port: 8080, BlockedPathPrefixes: []string{"/admin"}}
	record, err := routeDNSRecord(zone, input.Hostname, binding.Address, "route_k")
	if err != nil {
		t.Fatal(err)
	}
	st := &kubeRouteStore{zone: zone, binding: binding, op: RouteOperation{ID: "routeop_k", Kind: "apply", Status: "pending", Attempt: 1, Revision: 2, Principal: kubeRouteManager, DesiredRecord: record,
		Route: Route{ID: "route_k", OrgID: "org", ProjectID: "shop", Input: input, Binding: binding, Status: "pending", OperationID: "routeop_k", Revision: 1}}}
	kube := &fakeRouteCluster{log: &log, prerequisites: kubernetes.RoutePrerequisites{Ready: true}, status: kubernetes.RouteIngressStatus{Addresses: []string{"198.51.100.7"}}}
	svc := NewService(Deps{Store: st, Domains: st, Authz: cleanupTestAuth{}, Adapters: registry, Connections: routeDNSCredentials{"digitalocean"}, Resolver: addressResolver{[]string{"203.0.113.10"}}, ACMEDirectory: "https://acme-staging-v02.api.letsencrypt.org/directory"})
	svc.now = func() time.Time { return now }
	svc.kubernetesRoutes = func(_ context.Context, route Route, _ bool) (kubernetesRouteClient, error) {
		if route.Binding.ConnectionID != "conn_k8s" {
			return nil, ErrConflict
		}
		return kube, nil
	}
	svc.routeHTTPS = func(context.Context, string) (time.Time, int, error) { return now.AddDate(0, 3, 0), 200, nil }
	return svc, st, kube, dns
}

func TestKubernetesRouteRefusesAddressMismatch(t *testing.T) {
	svc, st, kube, dns := kubernetesRouteFixture(t) // binding Address 203.0.113.10, Ingress status reports 198.51.100.7
	err := svc.ReconcileRoute(t.Context(), "org", st.op.ID, nil)
	if !errors.Is(err, ErrPending) || dns.writes != 0 || !strings.Contains(st.op.Message, "198.51.100.7") || !strings.Contains(st.op.Message, "203.0.113.10") {
		t.Fatalf("%v %q writes=%d", err, st.op.Message, dns.writes)
	}
	if st.op.Message != "The ingress reports 198.51.100.7, but the connection declares 203.0.113.10. Update the Kubernetes connection's ingress address." || st.op.Status != "waiting" || st.op.DNSAttempted || st.published != 0 {
		t.Fatalf("operation: %+v", st.op)
	}
	if len(kube.applied) != 1 {
		t.Fatal("the Ingress was not applied before its status was read", kube.applied)
	}
	if status := DeriveRouteStatus(st.op.Route, st.zone, &st.op, svc.now()); status.DNS != DomainProvisioning || status.Message != st.op.Message {
		t.Fatalf("status: %+v", status)
	}

	kube.status.Addresses = nil
	if err = svc.ReconcileRoute(t.Context(), "org", st.op.ID, nil); !errors.Is(err, ErrPending) || dns.writes != 0 || st.op.Message != "Waiting for the ingress controller to publish an address." {
		t.Fatalf("no address yet: %v %q writes=%d", err, st.op.Message, dns.writes)
	}
}

func TestKubernetesRouteWritesSameReceiptsAsServers(t *testing.T) {
	svc, st, kube, dns := kubernetesRouteFixture(t)
	kube.status = kubernetes.RouteIngressStatus{Addresses: []string{"198.51.100.7", "203.0.113.10"}, Certificate: kubernetes.CertificateStatus{Issuing: true}, BackendReady: true}
	reachable := false
	svc.routeHTTPS = func(_ context.Context, host string) (time.Time, int, error) {
		if host != "api.example.com" {
			t.Fatal("probed another hostname", host)
		}
		if !reachable {
			return time.Time{}, 0, errors.New("certificate not issued yet")
		}
		return svc.now().AddDate(0, 3, 0), 200, nil
	}
	err := svc.ReconcileRoute(t.Context(), "org", st.op.ID, nil)
	if !errors.Is(err, ErrPending) || dns.writes != 1 || st.op.Receipt == nil || st.op.Receipt.Owner != "loomplane:route:route_k" || !st.op.DNSAttempted {
		t.Fatalf("first reconcile: %v writes=%d %+v", err, dns.writes, st.op.Receipt)
	}
	if record := dns.records[0]; record.Type != "A" || record.Value != "203.0.113.10" || record.Name != "api" {
		t.Fatalf("record: %+v", record)
	}
	if st.route == nil || st.route.Status != "waiting" || st.route.Receipt == nil || st.route.Verification.CertState != "issuing" || !st.route.Verification.DNS || st.route.Verification.TLS {
		t.Fatalf("published route: %+v", st.route)
	}
	want := kubernetes.RouteIngress{RouteID: "route_k", OrgID: "org", ProjectID: "shop", EnvironmentID: "prod", Hostname: "api.example.com", ServiceName: "lp-inst-gw", ServicePort: 8080,
		IngressClass: "nginx", Issuer: kubernetes.HTTPIssuerName, BlockedPathPrefixes: []string{"/admin"}}
	if len(kube.applied) != 1 || !reflect.DeepEqual(kube.applied[0], want) {
		t.Fatalf("ingress: %+v", kube.applied)
	}
	if kube.issuerClass != "nginx" || kube.directory != "https://acme-staging-v02.api.letsencrypt.org/directory" {
		t.Fatal("issuer", kube.issuerClass, kube.directory)
	}
	if !slices.Equal(*kube.log, []string{"prerequisites", "issuer", "ingress", "status route_k lp-inst-gw"}) {
		t.Fatal("apply order", *kube.log)
	}
	if !slices.Equal(st.locks, []string{"ingress/org/kubernetes:conn_k8s"}) {
		t.Fatal("lock", st.locks)
	}

	// The persisted receipt stops a second write.
	reachable = true
	kube.status.Certificate = kubernetes.CertificateStatus{Ready: true, Issuing: true}
	st.op.Status = "pending"
	if err = svc.ReconcileRoute(t.Context(), "org", st.op.ID, nil); err != nil {
		t.Fatal(err)
	}
	if dns.writes != 1 || st.op.Status != "succeeded" || st.route.Status != "ready" || st.route.Verification.CertState != "ready" || !st.route.Verification.TLS || !st.route.Verification.Backend {
		t.Fatalf("second reconcile: writes=%d %+v %+v", dns.writes, st.op, st.route)
	}
}

func TestKubernetesRouteVerifiesDestination(t *testing.T) {
	svc, st, kube, _ := kubernetesRouteFixture(t)
	kube.status = kubernetes.RouteIngressStatus{Addresses: []string{"203.0.113.10"}, Certificate: kubernetes.CertificateStatus{Ready: true}, BackendReady: true}
	for name, tc := range map[string]struct {
		resolved []string
		expires  time.Duration
		status   int
		ready    bool
		check    func(RouteVerification) bool
	}{
		"ready":                      {[]string{"203.0.113.10"}, 90 * 24 * time.Hour, 200, true, func(v RouteVerification) bool { return v.DNS && v.TLS && v.Backend }},
		"not only the ingress":       {[]string{"203.0.113.10", "192.0.2.1"}, 90 * 24 * time.Hour, 200, true, func(v RouteVerification) bool { return !v.DNS && v.DNSError != "" }},
		"a certificate about to end": {[]string{"203.0.113.10"}, time.Hour, 200, true, func(v RouteVerification) bool { return !v.TLS && v.TLSError != "" }},
		"a gateway error":            {[]string{"203.0.113.10"}, 90 * 24 * time.Hour, 503, true, func(v RouteVerification) bool { return v.TLS && !v.Backend }},
		"no ready endpoints":         {[]string{"203.0.113.10"}, 90 * 24 * time.Hour, 200, false, func(v RouteVerification) bool { return !v.Backend && v.BackendError != "" }},
	} {
		t.Run(name, func(t *testing.T) {
			svc.Resolver = addressResolver{tc.resolved}
			svc.routeHTTPS = func(context.Context, string) (time.Time, int, error) {
				return svc.now().Add(tc.expires), tc.status, nil
			}
			kube.status.BackendReady = tc.ready
			v := svc.verifyKubernetesRoute(t.Context(), st.op.Route, kube.status)
			if !tc.check(v) || v.CertState != "ready" {
				t.Fatalf("%+v", v)
			}
		})
	}
	svc.routeHTTPS = func(context.Context, string) (time.Time, int, error) {
		return time.Time{}, 0, errors.New("untrusted certificate")
	}
	failed := svc.verifyKubernetesRoute(t.Context(), st.op.Route, kubernetes.RouteIngressStatus{Addresses: []string{"198.51.100.7"}, BackendReady: true,
		Certificate: kubernetes.CertificateStatus{Failed: true, Message: "challenge refused"}})
	if failed.CertState != "failed" || failed.Backend || !strings.Contains(failed.TLSError, "challenge refused") || !strings.Contains(failed.BackendError, "198.51.100.7") {
		t.Fatalf("failed certificate and a moved address: %+v", failed)
	}
}

func TestKubernetesCertState(t *testing.T) {
	for want, status := range map[string]kubernetes.CertificateStatus{
		"failed":  {Failed: true, Issuing: true},
		"ready":   {Ready: true, Issuing: true},
		"issuing": {Issuing: true},
		"":        {},
	} {
		if got := kubernetesCertState(status); got != want {
			t.Fatalf("%+v: %q, want %q", status, got, want)
		}
	}
}

func TestKubernetesRouteUsesCNAMEForHostnameAddress(t *testing.T) {
	// Address "lb.example.net" -> DesiredRecord Type CNAME Value "lb.example.net"; at the apex -> ErrInvalid at preview.
	svc, st, _, _ := kubernetesRouteFixture(t)
	st.binding = kubernetesBinding("lb.example.net.")
	in := RouteInput{ZoneID: "zone", EnvironmentID: "prod", Service: "octo--gateway", Hostname: "api.example.com", Port: 8080}
	op, err := svc.PreviewRoute(t.Context(), kubeRouteManager, "shop", in)
	if err != nil {
		t.Fatal(err)
	}
	if op.DesiredRecord.Type != "CNAME" || op.DesiredRecord.Value != "lb.example.net" || op.DesiredRecord.Name != "api" || op.Route.Input.CloudID != "" || op.Route.Binding.BackendName() != RouteBackendKubernetes {
		t.Fatalf("preview: %+v", op)
	}
	in.Hostname = "example.com"
	if _, err = svc.PreviewRoute(t.Context(), kubeRouteManager, "shop", in); !errors.Is(err, ErrInvalid) {
		t.Fatal("CNAME at the zone apex", err)
	}
	// A server binding still needs a Cloud placement.
	st.binding = RouteBinding{ServerID: "srv_1", Address: "192.0.2.10"}
	in.Hostname = "api.example.com"
	if _, err = svc.PreviewRoute(t.Context(), kubeRouteManager, "shop", in); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), noCloudPlacement) {
		t.Fatal("server binding without a Cloud", err)
	}
}

func TestRouteDNSRecordForAddresses(t *testing.T) {
	zone := &Zone{Name: "example.com", ProviderZone: provider.DNSZone{ID: "zone"}}
	for address, want := range map[string]provider.DNSRecord{
		"203.0.113.10":    {ZoneID: "zone", Name: "api", Type: "A", Value: "203.0.113.10", TTL: 60},
		"2001:db8::1":     {ZoneID: "zone", Name: "api", Type: "AAAA", Value: "2001:db8::1", TTL: 60},
		"lb.example.net.": {ZoneID: "zone", Name: "api", Type: "CNAME", Value: "lb.example.net", TTL: 60},
	} {
		if got, err := routeDNSRecord(zone, "api.example.com", address, "route"); err != nil || got != want {
			t.Fatalf("%s: %+v %v", address, got, err)
		}
	}
	for _, address := range []string{"", "not an address", "*.example.net"} {
		if _, err := routeDNSRecord(zone, "api.example.com", address, "route"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q accepted: %v", address, err)
		}
	}
	if _, err := routeDNSRecord(zone, "example.com", "lb.example.net", "route"); !errors.Is(err, ErrInvalid) {
		t.Fatal("CNAME at the apex", err)
	}
	if got, err := routeDNSRecord(zone, "example.com", "203.0.113.10", "route"); err != nil || got.Type != "A" || got.Name != "" {
		t.Fatal("an A record at the apex", got, err)
	}
}

func TestKubernetesRouteRemoveDeletesIngressThenDNS(t *testing.T) {
	svc, st, kube, dns := kubernetesRouteFixture(t)
	receipt := provider.DNSRecord{ID: "record_1", ZoneID: "zone", Name: "api", Type: "A", Value: "203.0.113.10", TTL: 60, Owner: "loomplane:route:route_k"}
	dns.records = []provider.DNSRecord{receipt, {ID: "unrelated", ZoneID: "zone", Name: "mail", Type: "TXT", Value: "keep", TTL: 60}}
	route := st.op.Route
	route.Status, route.Receipt, route.Revision = "ready", &receipt, 3
	st.route = &route
	op, err := svc.RemoveRoute(t.Context(), kubeRouteManager, "shop", "route_k", 3)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(st.locks, []string{"ingress/org/kubernetes:conn_k8s"}) {
		t.Fatal("remove lock", st.locks)
	}
	// An accepted removal keeps its authority after access is revoked.
	svc.Authz = overviewAuth{}
	if err = svc.ReconcileRoute(t.Context(), "org", op.ID, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*kube.log, []string{"delete ingress route_k", "delete dns record_1"}) {
		t.Fatal("removal order", *kube.log)
	}
	if st.route.Status != "removed" || st.op.Status != "succeeded" || len(dns.records) != 1 || dns.records[0].ID != "unrelated" {
		t.Fatalf("after removal: %+v %+v", st.route, dns.records)
	}
}

func TestKubernetesRouteMissingCertManagerFailsClearly(t *testing.T) {
	svc, st, kube, dns := kubernetesRouteFixture(t)
	kube.prerequisites = kubernetes.RoutePrerequisites{Missing: []string{"cert-manager.io/v1 certificates", "cert-manager.io/v1 issuers"}}
	if err := svc.ReconcileRoute(t.Context(), "org", st.op.ID, nil); err != nil {
		t.Fatal(err)
	}
	if st.op.Status != "failed" || st.op.Message != "This cluster can't serve routes yet: cert-manager.io/v1 certificates, cert-manager.io/v1 issuers." {
		t.Fatalf("%+v", st.op)
	}
	if !slices.Equal(*kube.log, []string{"prerequisites"}) || dns.writes != 0 || st.published != 0 {
		t.Fatal("work after a missing prerequisite", *kube.log, dns.writes, st.published)
	}
}

func TestKubernetesRouteRefusesOtherKinds(t *testing.T) {
	svc, st, kube, _ := kubernetesRouteFixture(t)
	for _, kind := range []string{"prepare", "cutover", "rollback", "release"} {
		op := st.op
		op.Kind = kind
		err := svc.executeKubernetesRoute(t.Context(), st, &op, st.zone)
		var unsupported *provider.UnsupportedCapabilityError
		if !errors.As(err, &unsupported) || unsupported.Provider != "kubernetes" || unsupported.Capability != provider.IngressManagement {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	if len(*kube.log) != 0 {
		t.Fatal("an unsupported kind reached the cluster", *kube.log)
	}
}

func TestRouteLockKey(t *testing.T) {
	server := Route{OrgID: "org", Binding: RouteBinding{ServerID: "srv_1", ConnectionID: "conn_srv"}}
	if got := routeLockKey(server); got != "ingress/org/srv_1" {
		t.Fatal(got)
	}
	server.Binding.Backend = RouteBackendServer
	if got := routeLockKey(server); got != "ingress/org/srv_1" {
		t.Fatal(got)
	}
	if got := routeLockKey(Route{OrgID: "org", Binding: kubernetesBinding("203.0.113.10")}); got != "ingress/org/kubernetes:conn_k8s" {
		t.Fatal(got)
	}
}

// untouchedRouteStore fails any call a skipped route must not make.
type untouchedRouteStore struct {
	DomainStore
	t *testing.T
}

func (s untouchedRouteStore) RouteOperation(context.Context, string, string) (*RouteOperation, error) {
	s.t.Fatal("read an operation for a Kubernetes route")
	return nil, ErrNotFound
}
func (s untouchedRouteStore) AcceptRoute(context.Context, *RouteOperation) error {
	s.t.Fatal("created an operation for a Kubernetes route")
	return nil
}

func TestReleaseRoutesSkipKubernetes(t *testing.T) {
	// ReconcileReleaseRoutes with one Kubernetes route returns nil and creates no operation.
	st := untouchedRouteStore{t: t}
	svc := NewService(Deps{Domains: st, Authz: cleanupTestAuth{}})
	route := Route{ID: "route_k", OrgID: "org", ProjectID: "shop", Status: "ready", Input: RouteInput{EnvironmentID: "prod", Service: "web"}, Binding: kubernetesBinding("203.0.113.10")}
	in := ReleaseRouteRequest{Key: "plan/action", ProjectID: "shop", EnvironmentID: "prod", Service: "web", InstanceID: "inst_gw", Routes: []Route{route}, DrainSeconds: 60}
	if err := svc.ReconcileReleaseRoutes(t.Context(), kubeRouteManager, in); err != nil {
		t.Fatal(err)
	}
	// A Cloud compensation restoring the service leaves it alone too.
	if err := svc.ReconcileCompensationRoutes(t.Context(), kubeRouteManager, in); err != nil {
		t.Fatal(err)
	}
}

func TestKubernetesHandoffIsUnsupported(t *testing.T) {
	// PrepareRouteHandoff on a Kubernetes route -> ErrInfrastructureUnsupported.
	svc, st, _, _ := kubernetesRouteFixture(t)
	route := st.op.Route
	route.Status, route.Revision = "ready", 2
	st.route = &route
	if _, err := svc.PrepareRouteHandoff(t.Context(), kubeRouteManager, "shop", "route_k", HandoffInput{Revision: 2}); !errors.Is(err, provider.ErrInfrastructureUnsupported) {
		t.Fatal(err)
	}
	// A server route can't hand off to a Kubernetes binding either.
	route.Binding = RouteBinding{ServerID: "srv_1", Address: "192.0.2.10"}
	st.route = &route
	if _, err := svc.PrepareRouteHandoff(t.Context(), kubeRouteManager, "shop", "route_k", HandoffInput{Revision: 2, DestinationCloudID: "cloud_1"}); !errors.Is(err, provider.ErrInfrastructureUnsupported) {
		t.Fatal(err)
	}
}

func TestIngressAddressMismatchComparesAddresses(t *testing.T) {
	for _, tc := range []struct {
		reported []string
		declared string
		matches  bool
	}{
		{[]string{"2001:db8:0::1"}, "2001:db8::1", true},
		{[]string{"LB.Example.NET."}, "lb.example.net", true},
		{[]string{"198.51.100.7", "lb.example.net"}, "lb.example.net", true},
		{[]string{"lb.example.net"}, "203.0.113.10", false},
		{[]string{"203.0.113.1"}, "203.0.113.10", false},
	} {
		if got := ingressAddressMismatch(kubernetes.RouteIngressStatus{Addresses: tc.reported}, tc.declared) == ""; got != tc.matches {
			t.Fatalf("%v against %s: %v", tc.reported, tc.declared, got)
		}
	}
}

// challengeRouteDNS is a Cloudflare zone's records plus what each
// challenge token reaches.
type challengeRouteDNS struct {
	orderedDNS
	scopes map[string]cloudflare.ChallengeScope
}

func (d challengeRouteDNS) ChallengeScope(_ context.Context, c provider.InfrastructureCredential, _ string) (cloudflare.ChallengeScope, error) {
	scope, ok := d.scopes[c.Token]
	if !ok {
		return cloudflare.ChallengeScope{}, cloudflare.ErrRejected
	}
	return scope, nil
}

// namedResolver records the names it is asked for.
type namedResolver struct {
	addressResolver
	names *[]string
}

func (r namedResolver) LookupIPAddr(ctx context.Context, name string) ([]net.IPAddr, error) {
	*r.names = append(*r.names, name)
	return r.addressResolver.LookupIPAddr(ctx, name)
}

func TestKubernetesWildcardUsesDNSIssuer(t *testing.T) {
	svc, st, kube, dns := kubernetesRouteFixture(t)
	log := kube.log
	zone := st.zone
	zone.Provider, zone.ConnectionID, zone.ChallengeConnectionID = "cloudflare", "dns", "cf-dns"
	cf := func(id string) *connections.Connection {
		return &connections.Connection{ID: id, OrgID: "org", Cloud: connections.Cloudflare, Generation: 1, Status: "ready", Details: map[string]string{"account_id": "account"}}
	}
	conns := challengeConnections{"dns": cf("dns"), "cf-dns": cf("cf-dns")}
	scopes := map[string]cloudflare.ChallengeScope{tokenFor("cf-dns", 1): zoneScoped("zone")}
	registry := NewAdapters()
	if err := registry.RegisterDNS("cloudflare", challengeRouteDNS{orderedDNS{dns, log}, scopes}); err != nil {
		t.Fatal(err)
	}
	svc.Connections, svc.Adapters = conns, registry
	names := []string{}
	svc.Resolver = namedResolver{addressResolver{[]string{"203.0.113.10"}}, &names}
	probed := []string{}
	svc.routeHTTPS = func(_ context.Context, host string) (time.Time, int, error) {
		probed = append(probed, host)
		return svc.now().AddDate(0, 3, 0), 200, nil
	}
	kube.status = kubernetes.RouteIngressStatus{Addresses: []string{"203.0.113.10"}, Certificate: kubernetes.CertificateStatus{Ready: true}, BackendReady: true}

	// A Kubernetes binding previews a wildcard with nothing to announce.
	in := RouteInput{ZoneID: "zone", EnvironmentID: "prod", Service: "octo--gateway", Hostname: "*.example.com", Port: 8080}
	preview, err := svc.PreviewRoute(t.Context(), kubeRouteManager, "shop", in)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Notices != nil || preview.Route.Input.Hostname != "*.example.com" || preview.DesiredRecord.Name != "*" || preview.DesiredRecord.Type != "A" {
		t.Fatalf("preview: %q %+v", preview.Notices, preview.DesiredRecord)
	}

	op := st.op
	op.ID, op.Kind, op.Status, op.Route.OperationID = "routeop_w", "apply", "pending", "routeop_w"
	op.Route.ID, op.Route.Input, op.DesiredRecord, op.Notices = "route_w", preview.Route.Input, preview.DesiredRecord, nil
	op.DesiredRecord.Owner = "loomplane:route:route_w"
	st.op = op
	*log = (*log)[:0]
	if err = svc.ReconcileRoute(t.Context(), "org", "routeop_w", nil); err != nil {
		t.Fatal(err, st.op.Message)
	}
	if !slices.Equal(*log, []string{"prerequisites", "dns issuer zone example.com", "ingress", "status route_w lp-inst-gw"}) {
		t.Fatal("apply order", *log)
	}
	if !slices.Equal(kube.dnsTokens, []string{tokenFor("cf-dns", 1)}) || kube.directory != "https://acme-staging-v02.api.letsencrypt.org/directory" {
		t.Fatal("token", kube.dnsTokens, kube.directory)
	}
	if applied := kube.applied[len(kube.applied)-1]; applied.Hostname != "*.example.com" || applied.Issuer != kubernetes.DNSIssuerName("zone") {
		t.Fatalf("ingress %+v", applied)
	}
	// The wildcard is checked through one name under it.
	probe := routeProbeHostname(st.op.Route)
	if !strings.HasPrefix(probe, "lp-probe-") || !strings.HasSuffix(probe, ".example.com") || !slices.Equal(probed, []string{probe}) || !slices.Equal(names, []string{probe}) {
		t.Fatal("probed", probe, probed, names)
	}
	if st.route.Status != "ready" || dns.records[len(dns.records)-1].Name != "*" {
		t.Fatalf("route %+v", st.route)
	}

	// A check after the challenge connection was replaced moves the new
	// token to the cluster once it passes the scope checks again.
	verify := func() {
		t.Helper()
		*log = (*log)[:0]
		st.op.ID, st.op.Kind, st.op.Status = "routeop_v", "verify", "pending"
		_ = svc.ReconcileRoute(t.Context(), "org", "routeop_v", nil)
	}
	conns["cf-dns"].Generation = 2
	scopes[tokenFor("cf-dns", 2)] = zoneScoped("zone")
	verify()
	if st.op.Status != "succeeded" || kube.dnsTokens[len(kube.dnsTokens)-1] != tokenFor("cf-dns", 2) || !slices.Contains(*log, "dns issuer zone example.com") {
		t.Fatal("rotated token", st.op.Status, st.op.Message, kube.dnsTokens, *log)
	}

	// Widened at Cloudflare: the token stays off the cluster.
	widened := zoneScoped("zone", "zone_2")
	scopes[tokenFor("cf-dns", 2)] = widened
	before := len(kube.dnsTokens)
	verify()
	if st.op.Status != "failed" || len(kube.dnsTokens) != before || !strings.Contains(st.op.Message, "didn't put its token on the cluster") || strings.Contains(st.op.Message, tokenFor("cf-dns", 1)) {
		t.Fatal("widened token", st.op.Status, st.op.Message, kube.dnsTokens)
	}

	// Removing the wildcard drops the zone's issuer once no route uses it.
	scopes[tokenFor("cf-dns", 2)] = zoneScoped("zone")
	route := st.op.Route
	route.Status, route.Revision = "ready", 3
	st.route = &route
	removal, err := svc.RemoveRoute(t.Context(), kubeRouteManager, "shop", "route_w", 3)
	if err != nil {
		t.Fatal(err)
	}
	*log = (*log)[:0]
	if err = svc.ReconcileRoute(t.Context(), "org", removal.ID, nil); err != nil {
		t.Fatal(err)
	}
	if len(*log) != 3 || (*log)[0] != "delete ingress route_w" || (*log)[1] != "remove dns issuer zone" || !strings.HasPrefix((*log)[2], "delete dns ") {
		t.Fatal("removal order", *log)
	}
}

// A wildcard accepted on a zone inside a larger Cloudflare zone never puts
// the challenge token on the cluster.
func TestKubernetesWildcardRefusesAZoneInsideAnother(t *testing.T) {
	svc, st, kube, dns := kubernetesRouteFixture(t)
	zone := st.zone
	zone.Name, zone.ProofName = "shop.example.com", "_loomplane.shop.example.com"
	zone.Provider, zone.ConnectionID, zone.ChallengeConnectionID = "cloudflare", "dns", "cf-dns"
	cf := func(id string) *connections.Connection {
		return &connections.Connection{ID: id, OrgID: "org", Cloud: connections.Cloudflare, Generation: 1, Status: "ready", Details: map[string]string{"account_id": "account"}}
	}
	svc.Connections = challengeConnections{"dns": cf("dns"), "cf-dns": cf("cf-dns")}
	registry := NewAdapters()
	if err := registry.RegisterDNS("cloudflare", challengeRouteDNS{orderedDNS{dns, kube.log}, map[string]cloudflare.ChallengeScope{tokenFor("cf-dns", 1): zoneScoped("zone")}}); err != nil {
		t.Fatal(err)
	}
	svc.Adapters = registry
	op := st.op
	op.Route.Input.Hostname = "*.shop.example.com"
	op.DesiredRecord.Name = "*.shop"
	st.op = op
	if err := svc.ReconcileRoute(t.Context(), "org", op.ID, nil); err != nil {
		t.Fatal(err)
	}
	if st.op.Status != "failed" || len(kube.dnsTokens) != 0 || dns.writes != 0 || !strings.Contains(st.op.Message, "didn't put its token on the cluster") {
		t.Fatalf("status=%s tokens=%v writes=%d %s", st.op.Status, kube.dnsTokens, dns.writes, st.op.Message)
	}
}

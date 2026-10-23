package deployments

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/dbgateway"
	"github.com/xraph/loomplane/internal/dbgateway/plane"
	"github.com/xraph/loomplane/internal/gateway"
	"github.com/xraph/loomplane/internal/planning"
)

// fakeDBGateways is an in-memory DatabaseGatewayStore. ensure, when set,
// runs before EnsureGateway stores anything, as another deploy would.
type fakeDBGateways struct {
	mu       sync.Mutex
	gateways []dbgateway.Gateway
	routes   map[string][]string
	ensures  int
	ensure   func(f *fakeDBGateways, g dbgateway.Gateway)
	// unreported stores the gateway but reports it as not created, like a
	// store that couldn't read how many rows it inserted.
	unreported bool
	// failDelete is how many DeleteGateway calls fail before one works.
	failDelete int
}

func (f *fakeDBGateways) Gateways(_ context.Context, org, env string) ([]dbgateway.Gateway, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []dbgateway.Gateway
	for _, g := range f.gateways {
		if g.OrgID == org && g.EnvironmentID == env {
			out = append(out, g)
		}
	}
	return out, nil
}

func (f *fakeDBGateways) EnsureGateway(_ context.Context, g dbgateway.Gateway) (dbgateway.Gateway, bool, error) {
	if f.ensure != nil {
		f.ensure(f, g)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensures++
	for _, s := range f.gateways {
		if s.OrgID == g.OrgID && s.EnvironmentID == g.EnvironmentID && s.Name == g.Name {
			return s, false, nil
		}
	}
	f.gateways = append(f.gateways, g)
	return g, !f.unreported, nil
}

func (f *fakeDBGateways) UpdateGatewayPlacement(_ context.Context, org, id, provider, region, suffix string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.gateways {
		if f.gateways[i].OrgID == org && f.gateways[i].ID == id {
			f.gateways[i].Provider, f.gateways[i].Region, f.gateways[i].Suffix = provider, region, suffix
			return nil
		}
	}
	return dbgateway.ErrNotFound
}

func (f *fakeDBGateways) DeleteGateway(_ context.Context, org, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failDelete > 0 {
		f.failDelete--
		return errors.New("the database is down")
	}
	f.gateways = slices.DeleteFunc(f.gateways, func(g dbgateway.Gateway) bool { return g.OrgID == org && g.ID == id })
	delete(f.routes, id)
	return nil
}

func (f *fakeDBGateways) DeleteRoutesExcept(_ context.Context, _, gatewayID string, keep []string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	before := len(f.routes[gatewayID])
	f.routes[gatewayID] = slices.DeleteFunc(f.routes[gatewayID], func(r string) bool { return !slices.Contains(keep, r) })
	return before - len(f.routes[gatewayID]), nil
}

func (f *fakeDBGateways) EnsureRoute(_ context.Context, _, gatewayID, resource string) (dbgateway.RouteRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.routes == nil {
		f.routes = map[string][]string{}
	}
	if !slices.Contains(f.routes[gatewayID], resource) {
		f.routes[gatewayID] = append(f.routes[gatewayID], resource)
	}
	return dbgateway.RouteRecord{Resource: resource, State: "active"}, nil
}

func testTrustKey(t *testing.T) plane.TrustKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	return plane.TrustKey{PublicKey: pub, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour)}
}

func dbGatewayAction() planning.Action {
	return planning.Action{
		ID: "service:db-gateway", Op: planning.Create, Subject: planning.ServiceSubject, Name: "db-gateway",
		Provider: "cloud-run", Region: "us-central1", Step: 1,
		Service: &planning.ServiceConfig{
			Provider: "cloud-run", Region: "us-central1", Source: ".", Ecosystem: "go", Image: dbgateway.Dockerfile, Port: dbgateway.Port, Public: true,
			DatabaseGateway: &planning.DatabaseGatewayConfig{Name: "db-gateway", Suffix: "us-central1.db.example.com", Resources: []string{"orders", "users"}},
		},
	}
}

func dbGatewayRunner(store *fakeDBGateways, secrets snapshotSecrets, trust ...plane.TrustKey) *Runner {
	return NewRunner(RunnerDeps{
		Resources: secrets, DatabaseGateways: store, PublicURL: "https://loomplane.example.com/",
		DatabaseGatewayTrust: func() ([]plane.TrustKey, error) { return trust, nil },
	})
}

var gatewayDeployment = &Deployment{ID: "dep_1", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Environment: "production"}

// gatewayConfig reads env the way the gateway does.
func gatewayConfig(t *testing.T, env map[string]string) plane.Config {
	t.Helper()
	cfg, err := plane.ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("the gateway refuses its environment: %v", err)
	}
	return cfg
}

func TestEnsureDatabaseGatewayMintsOnce(t *testing.T) {
	store, secrets, trust := &fakeDBGateways{}, snapshotSecrets{}, testTrustKey(t)
	r := dbGatewayRunner(store, secrets, trust)
	a := dbGatewayAction()
	env, secret, err := r.ensureDatabaseGateway(t.Context(), gatewayDeployment, &a)
	if err != nil {
		t.Fatal(err)
	}
	cfg := gatewayConfig(t, env)
	if len(store.gateways) != 1 {
		t.Fatalf("recorded %d gateways", len(store.gateways))
	}
	gw := store.gateways[0]
	if cfg.GatewayID != gw.ID || !strings.HasPrefix(gw.ID, "dbgw_") || string(cfg.Suffix) != "us-central1.db.example.com" ||
		cfg.ControlURL != "https://loomplane.example.com" {
		t.Fatalf("config %+v for gateway %+v", cfg, gw)
	}
	if len(cfg.TrustKeys) != 1 || !bytes.Equal(cfg.TrustKeys[0].PublicKey, trust.PublicKey) ||
		!cfg.TrustKeys[0].NotBefore.Equal(trust.NotBefore) || !cfg.TrustKeys[0].NotAfter.Equal(trust.NotAfter) {
		t.Fatalf("trust keys %+v, want %+v", cfg.TrustKeys, trust)
	}
	if gw.ProjectID != "proj_1" || gw.Name != "db-gateway" || gw.Provider != "cloud-run" || gw.Region != "us-central1" || gw.Suffix != "us-central1.db.example.com" {
		t.Fatalf("recorded %+v", gw)
	}
	if !bytes.Equal(cfg.SigningKey.Public().(ed25519.PublicKey), gw.SigningPublicKey) || !bytes.Equal(cfg.SealingKey.PublicKey().Bytes(), gw.SealingPublicKey.Bytes()) {
		t.Fatal("the gateway's private keys don't match the public ones recorded")
	}
	// Vault holds the private halves under the gateway's id.
	if seed := secrets[dbGatewayKey("org_1", "env_1", gw.ID, "signing_key")]; !bytes.Equal(seed, cfg.SigningKey.Seed()) {
		t.Fatal("Vault doesn't hold the signing seed")
	}
	if raw := secrets[dbGatewayKey("org_1", "env_1", gw.ID, "sealing_key")]; !bytes.Equal(raw, cfg.SealingKey.Bytes()) {
		t.Fatal("Vault doesn't hold the sealing key")
	}
	if !secret["LOOMPLANE_DBGW_SIGNING_KEY"] || !secret["LOOMPLANE_DBGW_SEALING_KEY"] || len(secret) != 2 {
		t.Fatalf("secret %v", secret)
	}
	if got := store.routes[gw.ID]; !slices.Equal(got, []string{"orders", "users"}) {
		t.Fatalf("routes %v", got)
	}

	// The next deploy reuses the keys, and mints nothing.
	again, _, err := r.ensureDatabaseGateway(t.Context(), gatewayDeployment, &a)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"LOOMPLANE_DBGW_ID", "LOOMPLANE_DBGW_SIGNING_KEY", "LOOMPLANE_DBGW_SEALING_KEY"} {
		if again[k] != env[k] {
			t.Fatalf("%s changed on the second deploy", k)
		}
	}
	if len(store.gateways) != 1 || store.ensures != 1 || len(secrets) != 2 {
		t.Fatalf("second deploy minted: %d gateways, %d ensures, %d Vault entries", len(store.gateways), store.ensures, len(secrets))
	}
}

// mintGateway records a gateway with keys of its own, and keeps them in
// secrets, as a deploy that got there first would.
func mintGateway(t *testing.T, secrets snapshotSecrets, g dbgateway.Gateway) (dbgateway.Gateway, ed25519.PrivateKey, *ecdh.PrivateKey) {
	t.Helper()
	_, signing, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sealing, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	g.ID = "dbgw_winner"
	g.SigningPublicKey, g.SealingPublicKey = signing.Public().(ed25519.PublicKey), sealing.PublicKey()
	if secrets != nil {
		secrets[dbGatewayKey(g.OrgID, g.EnvironmentID, g.ID, "signing_key")] = signing.Seed()
		secrets[dbGatewayKey(g.OrgID, g.EnvironmentID, g.ID, "sealing_key")] = sealing.Bytes()
	}
	return g, signing, sealing
}

func TestEnsureDatabaseGatewayLosingARaceUsesTheWinnersKeys(t *testing.T) {
	secrets := snapshotSecrets{}
	var signing ed25519.PrivateKey
	var sealing *ecdh.PrivateKey
	var candidate string
	store := &fakeDBGateways{ensure: func(f *fakeDBGateways, g dbgateway.Gateway) {
		// The candidate's keys are in Vault before it's recorded.
		candidate = g.ID
		if secrets[dbGatewayKey(g.OrgID, g.EnvironmentID, g.ID, "signing_key")] == nil || secrets[dbGatewayKey(g.OrgID, g.EnvironmentID, g.ID, "sealing_key")] == nil {
			t.Error("the gateway was recorded before its keys were kept")
		}
		var winner dbgateway.Gateway
		winner, signing, sealing = mintGateway(t, secrets, g)
		f.gateways = append(f.gateways, winner)
	}}
	r := dbGatewayRunner(store, secrets, testTrustKey(t))
	a := dbGatewayAction()
	env, _, err := r.ensureDatabaseGateway(t.Context(), gatewayDeployment, &a)
	if err != nil {
		t.Fatal(err)
	}
	cfg := gatewayConfig(t, env)
	if cfg.GatewayID != "dbgw_winner" || !bytes.Equal(cfg.SigningKey.Seed(), signing.Seed()) || !bytes.Equal(cfg.SealingKey.Bytes(), sealing.Bytes()) {
		t.Fatal("the losing deploy didn't take the winner's identity")
	}
	for k := range secrets {
		if strings.Contains(k, candidate) {
			t.Fatalf("the losing candidate's key %s stayed in Vault", k)
		}
	}
	if len(secrets) != 2 || !slices.Equal(store.routes["dbgw_winner"], []string{"orders", "users"}) {
		t.Fatalf("Vault %d entries, routes %v", len(secrets), store.routes)
	}
}

func TestEnsureDatabaseGatewayKeepsKeysTheStoreRecordedQuietly(t *testing.T) {
	store, secrets := &fakeDBGateways{unreported: true}, snapshotSecrets{}
	r := dbGatewayRunner(store, secrets, testTrustKey(t))
	a := dbGatewayAction()
	env, _, err := r.ensureDatabaseGateway(t.Context(), gatewayDeployment, &a)
	if err != nil {
		t.Fatal(err)
	}
	if cfg := gatewayConfig(t, env); cfg.GatewayID != store.gateways[0].ID || len(secrets) != 2 {
		t.Fatalf("dropped the keys of the gateway it recorded: %d Vault entries", len(secrets))
	}
}

func TestEnsureDatabaseGatewayRefusesToRemintForARecordedGateway(t *testing.T) {
	gw, _, _ := mintGateway(t, nil, dbgateway.Gateway{OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Name: "db-gateway"})
	store, secrets := &fakeDBGateways{gateways: []dbgateway.Gateway{gw}}, snapshotSecrets{}
	r := dbGatewayRunner(store, secrets, testTrustKey(t))
	a := dbGatewayAction()
	_, _, err := r.ensureDatabaseGateway(t.Context(), gatewayDeployment, &a)
	if err == nil || !strings.Contains(err.Error(), "keys are missing") {
		t.Fatalf("err %v", err)
	}
	if store.ensures != 0 || len(secrets) != 0 || len(store.routes) != 0 {
		t.Fatalf("minted for a recorded gateway: %d ensures, %d Vault entries", store.ensures, len(secrets))
	}

	// Keys that aren't the recorded ones fail the same way, naming no key.
	other, signing, sealing := mintGateway(t, secrets, gw)
	_ = other
	_, _, err = r.ensureDatabaseGateway(t.Context(), gatewayDeployment, &a)
	if err == nil || store.ensures != 0 {
		t.Fatalf("accepted keys that don't match the record: %v", err)
	}
	for _, v := range [][]byte{signing.Seed(), sealing.Bytes()} {
		if strings.Contains(err.Error(), base64.StdEncoding.EncodeToString(v)) {
			t.Fatal("the error repeats a key")
		}
	}
}

func TestEnsureDatabaseGatewayNeedsASigningKey(t *testing.T) {
	for name, r := range map[string]*Runner{
		"no keys":  dbGatewayRunner(&fakeDBGateways{}, snapshotSecrets{}),
		"no trust": NewRunner(RunnerDeps{Resources: snapshotSecrets{}, DatabaseGateways: &fakeDBGateways{}, PublicURL: "https://loomplane.example.com"}),
	} {
		a := dbGatewayAction()
		_, _, err := r.ensureDatabaseGateway(t.Context(), gatewayDeployment, &a)
		if !errors.Is(err, dbgateway.ErrNotConfigured) || err.Error() != "the database gateway signing key is not configured" {
			t.Errorf("%s: %v", name, err)
		}
		if store := r.dbGateways.(*fakeDBGateways); store.ensures != 0 || len(r.resources.(snapshotSecrets)) != 0 {
			t.Errorf("%s: minted without a signing key", name)
		}
	}
}

// A deploy hands the gateway every key the plan recorded, and refuses when
// the keys changed after it was planned, before minting anything.
func TestEnsureDatabaseGatewayChecksThePlannedTrustKeys(t *testing.T) {
	old, next := testTrustKey(t), testTrustKey(t)
	next.NotBefore = next.NotBefore.Add(12 * time.Hour)
	store, secrets := &fakeDBGateways{}, snapshotSecrets{}
	r := dbGatewayRunner(store, secrets, old, next)
	a := dbGatewayAction()
	a.Service.DatabaseGateway.Trust = plane.TrustDigest([]plane.TrustKey{old, next})
	env, _, err := r.ensureDatabaseGateway(t.Context(), gatewayDeployment, &a)
	if err != nil {
		t.Fatal(err)
	}
	if cfg := gatewayConfig(t, env); plane.TrustDigest(cfg.TrustKeys) != a.Service.DatabaseGateway.Trust {
		t.Fatalf("the gateway trusts %+v", cfg.TrustKeys)
	}

	store, secrets = &fakeDBGateways{}, snapshotSecrets{}
	r = dbGatewayRunner(store, secrets, next)
	if _, _, err = r.ensureDatabaseGateway(t.Context(), gatewayDeployment, &a); !errors.Is(err, errDBGatewayTrustChanged) {
		t.Fatalf("err %v", err)
	}
	if store.ensures != 0 || len(secrets) != 0 {
		t.Fatal("minted with keys the plan didn't record")
	}
}

// A Loomplane whose database gateway service isn't wired in is a wiring
// error, not a missing signing key, and says so.
func TestEnsureDatabaseGatewayTellsAMissingServiceFromAMissingKey(t *testing.T) {
	store, secrets := &fakeDBGateways{}, snapshotSecrets{}
	r := NewRunner(RunnerDeps{
		Resources: secrets, DatabaseGateways: store, PublicURL: "https://loomplane.example.com",
		DatabaseGatewayTrust: func() ([]plane.TrustKey, error) { return nil, errors.New("not registered") },
	})
	a := dbGatewayAction()
	_, _, err := r.ensureDatabaseGateway(t.Context(), gatewayDeployment, &a)
	if err == nil || errors.Is(err, dbgateway.ErrNotConfigured) || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("err %v", err)
	}
	if store.ensures != 0 || len(secrets) != 0 {
		t.Fatal("minted without the service")
	}
}

// A signing key that has expired fails the deploy, and says that's why.
func TestEnsureDatabaseGatewayWithAnExpiredSigningKey(t *testing.T) {
	store, secrets := &fakeDBGateways{}, snapshotSecrets{}
	r := NewRunner(RunnerDeps{
		Resources: secrets, DatabaseGateways: store, PublicURL: "https://loomplane.example.com",
		DatabaseGatewayTrust: func() ([]plane.TrustKey, error) { return nil, dbgateway.ErrSigningKeyExpired },
	})
	a := dbGatewayAction()
	_, _, err := r.ensureDatabaseGateway(t.Context(), gatewayDeployment, &a)
	if !errors.Is(err, dbgateway.ErrSigningKeyExpired) || !strings.Contains(err.Error(), "has expired") {
		t.Fatalf("err %v", err)
	}
	if store.ensures != 0 || len(secrets) != 0 {
		t.Fatal("minted with an expired key")
	}
}

func TestEnsureDatabaseGatewayNeedsAnHTTPSPublicURL(t *testing.T) {
	for public, ok := range map[string]bool{
		"https://loomplane.example.com":        true,
		"http://localhost:8780":                true,
		"http://127.0.0.1:8780":                true,
		"http://loomplane.example.com":         false,
		"https://admin:hunter2@loomplane.test": false,
		"ftp://loomplane.example.com":          false,
		"":                                     false,
	} {
		store, secrets := &fakeDBGateways{}, snapshotSecrets{}
		r := dbGatewayRunner(store, secrets, testTrustKey(t))
		r.publicURL = public
		a := dbGatewayAction()
		env, _, err := r.ensureDatabaseGateway(t.Context(), gatewayDeployment, &a)
		switch {
		case ok && err != nil:
			t.Errorf("%q: %v", public, err)
		case ok:
			gatewayConfig(t, env)
		case err == nil:
			t.Errorf("%q: deployed a gateway that can't reach Loomplane", public)
		case strings.Contains(err.Error(), "hunter2"):
			t.Errorf("%q: the error repeats the password", public)
		case store.ensures != 0 || len(secrets) != 0:
			t.Errorf("%q: minted before refusing", public)
		}
	}
}

// gatewayBinding is the environment's database gateway binding, serving db.
func gatewayBinding(t *testing.T, url string) *planning.Binding {
	t.Helper()
	raw, err := json.Marshal(planning.ServiceConfig{DatabaseGateway: &planning.DatabaseGatewayConfig{
		Name: "db-gateway", Suffix: "nyc3.db.example.com", Resources: []string{"db"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return &planning.Binding{Subject: planning.ServiceSubject, Name: "db-gateway", URL: url, Config: raw}
}

const doDSN = "postgres://app:pw@db.do.example:25060/orders?sslmode=require"

// gatewayLink is a plan's link to db through the gateway gatewayBinding
// deploys.
var gatewayLink = planning.EnvLink{Key: "DATABASE_URL", From: "db", Subject: planning.ResourceSubject, Gateway: "nyc3.db.example.com"}

// A direct link gets the address kept for the resource, whatever the
// deployed gateway serves: an app planned off the gateway is never pointed
// back at it.
func TestResourceAddressPassThrough(t *testing.T) {
	secrets := snapshotSecrets{resourceKey("org_1", "env_1", "db", ResourceURL): []byte(doDSN), resourceKey("org_1", "env_1", "cache", ResourceURL): []byte("redis://:pw@cache:6379")}
	r := NewRunner(RunnerDeps{Resources: secrets})
	for name, services := range map[string]map[string]*planning.Binding{
		"no gateway":                 {"api": {URL: "https://api.example.com"}},
		"gateway serves others":      {"db-gateway": gatewayBinding(t, "https://db-gateway-xyz.run.app")},
		"opted out, gateway running": {"db-gateway": gatewayBinding(t, "https://db-gateway-xyz.run.app")},
	} {
		resource := "db"
		if name == "gateway serves others" {
			resource = "cache"
		}
		value, extra, err := r.resourceAddress(t.Context(), gatewayDeployment, "web", services, planning.EnvLink{Key: "DATABASE_URL", From: resource, Subject: planning.ResourceSubject})
		if err != nil || value != string(secrets[resourceKey("org_1", "env_1", resource, ResourceURL)]) || extra != nil {
			t.Errorf("%s: %q %v %v", name, value, extra, err)
		}
	}
}

func TestResourceAddressGateway(t *testing.T) {
	secrets := snapshotSecrets{resourceKey("org_1", "env_1", "db", ResourceURL): []byte(doDSN)}
	r := NewRunner(RunnerDeps{Resources: secrets})
	services := map[string]*planning.Binding{"db-gateway": gatewayBinding(t, "https://db-gateway-xyz.run.app"), "api": {URL: "https://api"}}
	value, extra, err := r.resourceAddress(t.Context(), gatewayDeployment, "web", services, gatewayLink)
	if err != nil {
		t.Fatal(err)
	}
	if value != "postgres://app:pw@db.nyc3.db.example.com:5432/orders?sslmode=require" {
		t.Fatalf("value %q", value)
	}
	if len(extra) != 2 || extra["DATABASE_URL_GATEWAY"] != (linkExtra{Value: "https://db-gateway-xyz.run.app"}) ||
		extra["DATABASE_URL_DIRECT"] != (linkExtra{Value: doDSN, Secret: true}) {
		t.Fatalf("extra %v", extra)
	}
}

// Nothing in the DSN's query can point the app past the gateway.
func TestResourceAddressGatewayDropsHostOverrides(t *testing.T) {
	dsn := "postgres://app:pw@db.do.example:25060/orders?sslmode=require&host=evil.example&hostaddr=10.0.0.1&port=6543"
	r := NewRunner(RunnerDeps{Resources: snapshotSecrets{resourceKey("org_1", "env_1", "db", ResourceURL): []byte(dsn)}})
	services := map[string]*planning.Binding{"db-gateway": gatewayBinding(t, "https://db-gateway-xyz.run.app")}
	value, _, err := r.resourceAddress(t.Context(), gatewayDeployment, "web", services, gatewayLink)
	if err != nil || value != "postgres://app:pw@db.nyc3.db.example.com:5432/orders?sslmode=require" {
		t.Fatalf("value %q: %v", value, err)
	}
}

// Each deploy keeps the gateway's routes and recorded placement in
// step with the plan, and never its keys.
func TestEnsureDatabaseGatewayKeepsRoutesAndPlacementCurrent(t *testing.T) {
	store, secrets := &fakeDBGateways{}, snapshotSecrets{}
	r := dbGatewayRunner(store, secrets, testTrustKey(t))
	a := dbGatewayAction()
	first, _, err := r.ensureDatabaseGateway(t.Context(), gatewayDeployment, &a)
	if err != nil {
		t.Fatal(err)
	}
	moved := dbGatewayAction()
	moved.Region, moved.Service.Region = "europe-west1", "europe-west1"
	moved.Service.DatabaseGateway.Suffix = "europe-west1.db.example.com"
	moved.Service.DatabaseGateway.Resources = []string{"orders"}
	again, _, err := r.ensureDatabaseGateway(t.Context(), gatewayDeployment, &moved)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing moves until the gateway is applied.
	gw := store.gateways[0]
	if !slices.Equal(store.routes[gw.ID], []string{"orders", "users"}) || gw.Region != "us-central1" {
		t.Fatalf("changed before the gateway was applied: routes %v, %+v", store.routes[gw.ID], gw)
	}
	if err = r.settleDatabaseGateway(t.Context(), gatewayDeployment, &moved); err != nil {
		t.Fatal(err)
	}
	gw = store.gateways[0]
	if !slices.Equal(store.routes[gw.ID], []string{"orders"}) {
		t.Fatalf("routes %v: the dropped resource kept its route", store.routes[gw.ID])
	}
	if gw.Region != "europe-west1" || gw.Suffix != "europe-west1.db.example.com" || gw.Provider != "cloud-run" {
		t.Fatalf("recorded placement %+v", gw)
	}
	if again["LOOMPLANE_DBGW_SUFFIX"] != "europe-west1.db.example.com" {
		t.Fatalf("suffix %q", again["LOOMPLANE_DBGW_SUFFIX"])
	}
	for _, k := range []string{"LOOMPLANE_DBGW_ID", "LOOMPLANE_DBGW_SIGNING_KEY", "LOOMPLANE_DBGW_SEALING_KEY"} {
		if again[k] != first[k] {
			t.Fatalf("%s changed when the gateway moved", k)
		}
	}
}

// removing is an executor whose Remove checks the database gateway is still
// recorded while its cloud service is removed, and can fail.
type removing struct {
	*recorder
	gateways *fakeDBGateways
	fail     error
	removed  int
	early    bool
}

func (e *removing) Remove(_ context.Context, job *Job, _ *planning.Binding) error {
	e.removed++
	if len(e.gateways.gateways) != 1 {
		e.early = true
	}
	if err := job.Checkpoint([]byte("rm")); err != nil {
		return err
	}
	return e.fail
}

// Deleting the database gateway removes its cloud service, then its
// record and keys, so deploying it again starts afresh.
func TestRunDeletingTheDatabaseGatewayRemovesItsRecordAndKeys(t *testing.T) {
	secrets := snapshotSecrets{}
	gw, _, _ := mintGateway(t, secrets, dbgateway.Gateway{OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Name: "db-gateway"})
	other, _, _ := mintGateway(t, nil, dbgateway.Gateway{OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_2", Name: "db-gateway"})
	other.ID = "dbgw_other_env"
	otherKey := dbGatewayKey("org_1", "env_2", other.ID, "signing_key")
	secrets[otherKey] = []byte("kept")
	cfg, err := json.Marshal(dbGatewayAction().Service)
	if err != nil {
		t.Fatal(err)
	}
	for _, fail := range []error{errors.New("permission denied"), nil} {
		store := &fakeDBGateways{gateways: []dbgateway.Gateway{gw}, routes: map[string][]string{gw.ID: {"orders"}}}
		exec := &removing{recorder: &recorder{}, gateways: store, fail: fail}
		plan := &planning.Plan{
			ID: "plan_del", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Environment: "production",
			Status: planning.StatusApproved, Executable: true, Connections: map[string]string{"gcp": "conn_g"},
			Actions: []planning.Action{{ID: "service:db-gateway", Op: planning.Delete, Subject: planning.ServiceSubject,
				Name: "db-gateway", Provider: "cloud-run", Region: "us-central1", ConnectionID: "conn_g", Step: 1}},
		}
		plans := &fakePlans{plan: plan, bindings: map[string]*planning.Binding{"service:db-gateway": {
			ID: "bind_gw", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Subject: planning.ServiceSubject, Name: "db-gateway",
			Provider: "cloud-run", Region: "us-central1", ConnectionID: "conn_g", ExternalID: "ext-db-gateway", Config: cfg,
		}}}
		r := NewRunner(RunnerDeps{
			Store: &memStore{d: map[string]*Deployment{}}, Plans: plans, Credentials: fakeCredentials{}, Variables: fakeVariables{},
			Sources: &fakeSources{}, Providers: available(t), Executors: map[string]Executor{"cloud-run": exec}, Resources: secrets,
			DatabaseGateways: store, PublicURL: "https://loomplane.example.com",
		})
		_, err := runAll(t, r, plan)
		if err != nil {
			t.Fatal(err)
		}
		signing, sealing := dbGatewayKey("org_1", "env_1", gw.ID, "signing_key"), dbGatewayKey("org_1", "env_1", gw.ID, "sealing_key")
		if fail != nil {
			// The cloud service is still there, so is everything it needs.
			if len(store.gateways) != 1 || secrets[signing] == nil || secrets[sealing] == nil || plans.bindings["service:db-gateway"] == nil {
				t.Fatal("a failed removal dropped the gateway's record or keys")
			}
			continue
		}
		if exec.removed != 1 || exec.early || len(store.gateways) != 0 || store.routes[gw.ID] != nil {
			t.Fatalf("removed %d, gateways %+v, routes %v", exec.removed, store.gateways, store.routes)
		}
		if _, ok := secrets[signing]; ok {
			t.Fatal("the signing key stayed in Vault")
		}
		if _, ok := secrets[sealing]; ok {
			t.Fatal("the sealing key stayed in Vault")
		}
		if string(secrets[otherKey]) != "kept" {
			t.Fatal("another environment's gateway lost its key")
		}
		if plans.bindings["service:db-gateway"] != nil {
			t.Fatal("the binding is still there")
		}
	}
}

func TestResourceAddressGatewayFailuresHoldNoSecrets(t *testing.T) {
	moved := gatewayLink
	moved.Gateway = "eu-west1.db.example.com"
	for name, tc := range map[string]struct {
		dsn, url, want string
		link           planning.EnvLink
		noGateway      bool
	}{
		"no address yet":     {dsn: doDSN, url: "", want: "web needs the database gateway, which has no address yet", link: gatewayLink},
		"not https":          {dsn: doDSN, url: "http://db-gateway-xyz.run.app", want: "isn't https", link: gatewayLink},
		"bad dsn":            {dsn: "postgres://app:pw@[db.do.example:25060/orders", url: "https://db-gateway-xyz.run.app", want: "isn't a URL", link: gatewayLink},
		"no gateway serving": {dsn: doDSN, want: "web reaches db through the database gateway, which doesn't serve it yet", link: gatewayLink, noGateway: true},
		"another suffix":     {dsn: doDSN, url: "https://db-gateway-xyz.run.app", want: "names its hosts under nyc3.db.example.com", link: moved},
	} {
		r := NewRunner(RunnerDeps{Resources: snapshotSecrets{resourceKey("org_1", "env_1", "db", ResourceURL): []byte(tc.dsn)}})
		services := map[string]*planning.Binding{"db-gateway": gatewayBinding(t, tc.url)}
		if tc.noGateway {
			services = map[string]*planning.Binding{"api": {URL: "https://api.example.com"}}
		}
		_, _, err := r.resourceAddress(t.Context(), gatewayDeployment, "web", services, tc.link)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if strings.Contains(err.Error(), "pw") {
			t.Errorf("%s: the error repeats the password: %v", name, err)
		}
	}
}

func TestAppliedInputsUseGatewayAddress(t *testing.T) {
	ctx := t.Context()
	secrets := snapshotSecrets{resourceKey("org_1", "env_1", "db", ResourceURL): []byte(doDSN)}
	r := NewRunner(RunnerDeps{Variables: fakeVariables{"svc": {"NAME": "web"}}, Resources: secrets})
	services := map[string]*planning.Binding{"db-gateway": gatewayBinding(t, "https://db-gateway-xyz.run.app")}
	resources := map[string]*planning.Binding{"db": {Subject: planning.ResourceSubject, Name: "db"}}
	a := planning.Action{ID: "service:web", Name: "web", Service: &planning.ServiceConfig{Service: "svc",
		Env: []planning.EnvLink{gatewayLink}}}
	if err := r.captureAppliedVariables(ctx, gatewayDeployment, &a, services); err != nil {
		t.Fatal(err)
	}
	want, extra, err := r.resourceAddress(ctx, gatewayDeployment, "web", services, gatewayLink)
	if err != nil {
		t.Fatal(err)
	}
	var captured appliedVariables
	if err = json.Unmarshal(secrets[a.Service.AppliedInputs.Variables], &captured); err != nil {
		t.Fatal(err)
	}
	if captured.ResourceValues["DATABASE_URL"] != want || captured.GatewayValues["DATABASE_URL_GATEWAY"] != extra["DATABASE_URL_GATEWAY"].Value {
		t.Fatalf("captured %+v, want %q %v", captured, want, extra)
	}
	env, secret, err := r.env(ctx, gatewayDeployment, a, services, resources)
	if err != nil {
		t.Fatal(err)
	}
	if env["DATABASE_URL"] != want || env["DATABASE_URL_GATEWAY"] != "https://db-gateway-xyz.run.app" || env["NAME"] != "web" {
		t.Fatalf("env %v", env)
	}
	if !secret["DATABASE_URL"] || secret["DATABASE_URL_GATEWAY"] {
		t.Fatalf("secret %v: the DSN is secret and the gateway's address isn't", secret)
	}

	// A variable someone set for the gateway's address wins, as any does.
	r.variables = fakeVariables{"svc": {"DATABASE_URL_GATEWAY": "https://mine"}}
	b := planning.Action{ID: "service:other", Name: "other", Service: &planning.ServiceConfig{Service: "svc", Env: a.Service.Env}}
	if err = r.captureAppliedVariables(ctx, gatewayDeployment, &b, services); err != nil {
		t.Fatal(err)
	}
	if env, _, err = r.env(ctx, gatewayDeployment, b, services, resources); err != nil || env["DATABASE_URL_GATEWAY"] != "https://mine" || env["DATABASE_URL"] != want {
		t.Fatalf("override lost: %v %v", env, err)
	}
}

// Without a pinned snapshot, the app's env reads the address afresh, and a
// variable someone set for the gateway's address still wins.
func TestEnvWithoutASnapshotPointsAtTheGateway(t *testing.T) {
	secrets := snapshotSecrets{resourceKey("org_1", "env_1", "db", ResourceURL): []byte(doDSN)}
	services := map[string]*planning.Binding{"db-gateway": gatewayBinding(t, "https://db-gateway-xyz.run.app")}
	resources := map[string]*planning.Binding{"db": {Subject: planning.ResourceSubject, Name: "db"}}
	a := planning.Action{ID: "service:web", Name: "web", Service: &planning.ServiceConfig{Service: "svc",
		Env: []planning.EnvLink{gatewayLink}}}
	for set, want := range map[string]string{"": "https://db-gateway-xyz.run.app", "https://mine": "https://mine"} {
		vars := fakeVariables{"svc": {}}
		if set != "" {
			vars["svc"]["DATABASE_URL_GATEWAY"] = set
		}
		r := NewRunner(RunnerDeps{Variables: vars, Resources: secrets})
		env, secret, err := r.env(t.Context(), gatewayDeployment, a, services, resources)
		if err != nil || env["DATABASE_URL"] != "postgres://app:pw@db.nyc3.db.example.com:5432/orders?sslmode=require" ||
			env["DATABASE_URL_GATEWAY"] != want || !secret["DATABASE_URL"] || secret["DATABASE_URL_GATEWAY"] {
			t.Errorf("set %q: env %v secret %v: %v", set, env, secret, err)
		}
	}

	// The link decides, not the deployed gateway: planned without it, the
	// app gets the database's own address while the gateway still serves
	// it.
	direct := a
	direct.Service = &planning.ServiceConfig{Service: "svc", Env: []planning.EnvLink{{Key: "DATABASE_URL", From: "db", Subject: planning.ResourceSubject}}}
	r := NewRunner(RunnerDeps{Variables: fakeVariables{"svc": {}}, Resources: secrets})
	env, secret, err := r.env(t.Context(), gatewayDeployment, direct, services, resources)
	if err != nil || env["DATABASE_URL"] != doDSN || !secret["DATABASE_URL"] || len(env) != 1 {
		t.Fatalf("direct link: env %v secret %v: %v", env, secret, err)
	}
}

// Both kinds of gateway are generated: neither is built from, pinned to or
// checked against a repository.
func TestGeneratedServicesAreNeverRepositoryServices(t *testing.T) {
	d := &Deployment{ID: "dep", OrgID: "org", EnvironmentID: "env", CommitSHA: strings.Repeat("a", 40),
		Sources: []SourceArtifact{{Alias: "app", CommitSHA: strings.Repeat("a", 40), Key: "k"}}}
	plan := &planning.Plan{Sources: []planning.PlanSource{{Alias: "app", CommitSHA: strings.Repeat("a", 40)}}}
	r := NewRunner(RunnerDeps{Resources: snapshotSecrets{}, Variables: fakeVariables{}})
	db := dbGatewayAction()
	edge := planning.Action{ID: "service:edge", Name: "edge", Service: &planning.ServiceConfig{Gateway: []gateway.Route{{Path: "/", To: "web", Env: "WEB_URL"}}}}
	for _, a := range []planning.Action{db, edge} {
		if commitFor(d, a) != "" {
			t.Errorf("%s has a commit", a.Name)
		}
		if err := r.captureAppliedVariables(t.Context(), d, &a, nil); err != nil || a.Service.AppliedInputs != nil {
			t.Errorf("%s captured variables: %v", a.Name, err)
		}
		pinAppliedSource(d, plan, &a)
		if a.Service.AppliedInputs != nil {
			t.Errorf("%s pinned a source", a.Name)
		}
		if err := r.CheckAppliedInputs(t.Context(), "org", "env", a.Service); err != nil {
			t.Errorf("%s needs applied inputs: %v", a.Name, err)
		}
	}
	// A repository service still has them.
	svc := planning.Action{ID: "service:web", Name: "web", Service: &planning.ServiceConfig{Repo: "app"}}
	if commitFor(d, svc) == "" || r.CheckAppliedInputs(t.Context(), "org", "env", svc.Service) == nil {
		t.Error("a repository service lost its commit or its applied-inputs check")
	}
}

// A deploy end to end: the database gateway first, built from its own
// source with the env it needs, then an app that reaches Postgres through
// it.
func TestRunDeploysTheDatabaseGatewayAndPointsAppsAtIt(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(srv.Close)
	exec := &recorder{url: srv.URL}
	gw := dbGatewayAction()
	gw.Service.DatabaseGateway.Resources = []string{"db"}
	plan := &planning.Plan{
		ID: "plan_1", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Environment: "production",
		Status: planning.StatusApproved, Executable: true,
		Sources: []planning.PlanSource{{SourceID: "src_1", Alias: "mono", Repository: "acme/mono", AnalysisID: "anl_1",
			CommitSHA: strings.Repeat("a", 40), InScope: true}},
		Connections: map[string]string{"gcp": "conn_g"},
		Actions: []planning.Action{gw, {
			ID: "service:api", Op: planning.Create, Subject: planning.ServiceSubject, Name: "api", Provider: "cloud-run", Step: 2,
			DependsOn: []string{gw.ID},
			Service: &planning.ServiceConfig{Provider: "cloud-run", Service: "svc:go:api", Public: true,
				Env: []planning.EnvLink{{Key: "DATABASE_URL", From: "db", Subject: planning.ResourceSubject, Gateway: "us-central1.db.example.com"}}},
		}},
	}
	secrets := snapshotSecrets{resourceKey("org_1", "env_1", "db", ResourceURL): []byte(doDSN)}
	store := &memStore{d: map[string]*Deployment{}}
	plans := &fakePlans{plan: plan, bindings: map[string]*planning.Binding{
		"resource:db": {ID: "bind_db", Subject: planning.ResourceSubject, Name: "db", Provider: "digitalocean"},
	}}
	trust := testTrustKey(t)
	gateways := &fakeDBGateways{}
	r := NewRunner(RunnerDeps{
		Store: store, Plans: plans, Credentials: fakeCredentials{}, Variables: fakeVariables{}, Sources: &fakeSources{},
		Providers: available(t), Executors: map[string]Executor{"cloud-run": exec}, Resources: secrets,
		DatabaseGateways: gateways, DatabaseGatewayTrust: func() ([]plane.TrustKey, error) { return []plane.TrustKey{trust}, nil },
		PublicURL: "https://loomplane.example.com",
	})
	r.http, r.verifyWait = srv.Client(), time.Second
	if _, err := runAll(t, r, plan); err != nil {
		t.Fatal(err)
	}
	if len(exec.jobs) != 2 || exec.jobs[0].Action.Name != "db-gateway" || exec.jobs[1].Action.Name != "api" {
		t.Fatalf("ran %d jobs", len(exec.jobs))
	}
	gwJob, apiJob := exec.jobs[0], exec.jobs[1]
	data, id, err := dbgateway.Source()
	if err != nil {
		t.Fatal(err)
	}
	if gwJob.SourceID != id || gwJob.CommitSHA != "" || exec.read["db-gateway"] != string(data) {
		t.Fatalf("the gateway wasn't built from its generated source: source %q commit %q", gwJob.SourceID, gwJob.CommitSHA)
	}
	if cfg := gatewayConfig(t, gwJob.Env); cfg.GatewayID != gateways.gateways[0].ID || !gwJob.Secrets["LOOMPLANE_DBGW_SIGNING_KEY"] {
		t.Fatalf("gateway config %+v", cfg)
	}
	if gwJob.Action.Service.AppliedInputs != nil {
		t.Fatal("the gateway was pinned like a repository service")
	}
	if !slices.Equal(gateways.routes[gateways.gateways[0].ID], []string{"db"}) {
		t.Fatalf("routes %v", gateways.routes)
	}
	gwURL := plans.bindings["service:db-gateway"].URL
	if apiJob.Env["DATABASE_URL"] != "postgres://app:pw@db.us-central1.db.example.com:5432/orders?sslmode=require" ||
		apiJob.Env["DATABASE_URL_GATEWAY"] != gwURL || !strings.HasPrefix(gwURL, "https://") || !apiJob.Secrets["DATABASE_URL"] {
		t.Fatalf("api env %v", apiJob.Env)
	}
	var captured appliedVariables
	if err = json.Unmarshal(secrets[apiJob.Action.Service.AppliedInputs.Variables], &captured); err != nil {
		t.Fatal(err)
	}
	if captured.ResourceValues["DATABASE_URL"] != apiJob.Env["DATABASE_URL"] || captured.GatewayValues["DATABASE_URL_GATEWAY"] != gwURL {
		t.Fatalf("captured %+v, the api got %v", captured, apiJob.Env)
	}
}

// tlsAnswering is an https server that answers 200, and a client for it.
func tlsAnswering(t *testing.T) (string, *http.Client) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(srv.Close)
	return srv.URL, srv.Client()
}

// A gateway whose Apply fails keeps the routes and placement it was
// recorded with, and the retry that applies it moves them.
func TestRunAFailedApplyLeavesTheGatewayRecordAlone(t *testing.T) {
	ctx := t.Context()
	secrets := snapshotSecrets{}
	gw, _, _ := mintGateway(t, secrets, dbgateway.Gateway{OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Name: "db-gateway",
		Provider: "cloud-run", Region: "us-central1", Suffix: "us-central1.db.example.com"})
	store := &fakeDBGateways{gateways: []dbgateway.Gateway{gw}, routes: map[string][]string{gw.ID: {"orders", "users"}}}
	url, client := tlsAnswering(t)
	exec := &recorder{url: url, fail: map[string]error{"db-gateway": errors.New("quota exceeded")}}
	moved := dbGatewayAction()
	moved.Op, moved.Region, moved.Service.Region = planning.Update, "europe-west1", "europe-west1"
	moved.Service.DatabaseGateway.Suffix = "europe-west1.db.example.com"
	moved.Service.DatabaseGateway.Resources = []string{"orders"}
	plan := &planning.Plan{
		ID: "plan_move", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Environment: "production",
		Status: planning.StatusApproved, Executable: true, Connections: map[string]string{"gcp": "conn_g"},
		Actions: []planning.Action{moved},
	}
	deployments := &memStore{d: map[string]*Deployment{}}
	r := NewRunner(RunnerDeps{
		Store: deployments, Plans: &fakePlans{plan: plan, bindings: map[string]*planning.Binding{}}, Credentials: fakeCredentials{},
		Variables: fakeVariables{}, Sources: &fakeSources{}, Providers: available(t), Executors: map[string]Executor{"cloud-run": exec},
		Resources: secrets, DatabaseGateways: store, PublicURL: "https://loomplane.example.com",
		DatabaseGatewayTrust: func() ([]plane.TrustKey, error) { return []plane.TrustKey{testTrustKey(t)}, nil },
	})
	r.http, r.verifyWait = client, time.Second
	id, err := runAll(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := deployments.GetDeployment(ctx, "org_1", id); d.Status != StatusFailed {
		t.Fatalf("deployment %s", d.Status)
	}
	if got := store.gateways[0]; !slices.Equal(store.routes[gw.ID], []string{"orders", "users"}) ||
		got.Region != "us-central1" || got.Suffix != "us-central1.db.example.com" {
		t.Fatalf("a failed apply moved the record: routes %v, %+v", store.routes[gw.ID], got)
	}

	// The retry applies it, and the record follows.
	exec.fail = nil
	if ok, rerr := deployments.Reopen(ctx, "org_1", id); !ok || rerr != nil {
		t.Fatalf("reopen: %v %v", ok, rerr)
	}
	if err = r.Run(ctx, "org_1", id, moved.ID, nil, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := store.gateways[0]; !slices.Equal(store.routes[gw.ID], []string{"orders"}) ||
		got.Region != "europe-west1" || got.Suffix != "europe-west1.db.example.com" {
		t.Fatalf("the applied gateway's record didn't follow: routes %v, %+v", store.routes[gw.ID], got)
	}
}

// Which gateway serves a resource doesn't depend on map order, and
// only a service binding can be one.
func TestServingGatewayIsDeterministic(t *testing.T) {
	asResource := gatewayBinding(t, "https://resource.example")
	asResource.Subject = planning.ResourceSubject
	zz, aa := gatewayBinding(t, "https://zz.example"), gatewayBinding(t, "https://aa.example")
	zz.Name, aa.Name = "zz-gateway", "aa-gateway"
	services := map[string]*planning.Binding{"zz-gateway": zz, "aa-gateway": aa, "0-resource": asResource}
	for range 50 {
		if b, suffix := servingGateway(services, "db"); b != aa || suffix != "nyc3.db.example.com" {
			t.Fatalf("served by %+v", b)
		}
	}
	if b, _ := servingGateway(map[string]*planning.Binding{"x": asResource}, "db"); b != nil {
		t.Fatal("a resource binding served as the gateway")
	}
}

// A delete that removed the keys but not the record is finished by its
// retry, which deletes the record and then the binding.
func TestRunRetriesAPartialDatabaseGatewayDelete(t *testing.T) {
	ctx := t.Context()
	secrets := snapshotSecrets{}
	gw, _, _ := mintGateway(t, secrets, dbgateway.Gateway{OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Name: "db-gateway"})
	store := &fakeDBGateways{gateways: []dbgateway.Gateway{gw}, routes: map[string][]string{gw.ID: {"orders"}}, failDelete: 1}
	exec := &removing{recorder: &recorder{}, gateways: store}
	cfg, err := json.Marshal(dbGatewayAction().Service)
	if err != nil {
		t.Fatal(err)
	}
	plan := &planning.Plan{
		ID: "plan_del", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Environment: "production",
		Status: planning.StatusApproved, Executable: true, Connections: map[string]string{"gcp": "conn_g"},
		Actions: []planning.Action{{ID: "service:db-gateway", Op: planning.Delete, Subject: planning.ServiceSubject,
			Name: "db-gateway", Provider: "cloud-run", Region: "us-central1", ConnectionID: "conn_g", Step: 1}},
	}
	plans := &fakePlans{plan: plan, bindings: map[string]*planning.Binding{"service:db-gateway": {
		ID: "bind_gw", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Subject: planning.ServiceSubject, Name: "db-gateway",
		Provider: "cloud-run", Region: "us-central1", ConnectionID: "conn_g", ExternalID: "ext-db-gateway", Config: cfg,
	}}}
	deployments := &memStore{d: map[string]*Deployment{}}
	r := NewRunner(RunnerDeps{
		Store: deployments, Plans: plans, Credentials: fakeCredentials{}, Variables: fakeVariables{},
		Sources: &fakeSources{}, Providers: available(t), Executors: map[string]Executor{"cloud-run": exec}, Resources: secrets,
		DatabaseGateways: store, PublicURL: "https://loomplane.example.com",
	})
	id, err := runAll(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := deployments.GetDeployment(ctx, "org_1", id); d.Status != StatusFailed || !strings.Contains(d.Error, "the database is down") {
		t.Fatalf("deployment %s: %s", d.Status, d.Error)
	}
	if len(store.gateways) != 1 || plans.bindings["service:db-gateway"] == nil || len(secrets) != 0 {
		t.Fatalf("after the failure: %d gateways, binding %v, %d Vault entries", len(store.gateways), plans.bindings["service:db-gateway"] != nil, len(secrets))
	}
	if ok, rerr := deployments.Reopen(ctx, "org_1", id); !ok || rerr != nil {
		t.Fatalf("reopen: %v %v", ok, rerr)
	}
	if err = r.Run(ctx, "org_1", id, "service:db-gateway", nil, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(store.gateways) != 0 || plans.bindings["service:db-gateway"] != nil || exec.removed != 2 {
		t.Fatalf("after the retry: %d gateways, binding %v, %d removes", len(store.gateways), plans.bindings["service:db-gateway"] != nil, exec.removed)
	}
}

// An app linked to a gateway-served database also gets the database's
// own address in <KEY>_DIRECT, for what speaks Postgres over TCP, such as a
// migration, which the gateway doesn't serve yet. It holds the credentials,
// so it's secret, and a variable someone set of that name wins.
func TestGatewayLinkedAppsGetTheirDirectAddress(t *testing.T) {
	ctx := t.Context()
	const gatewayDSN = "postgres://app:pw@db.nyc3.db.example.com:5432/orders?sslmode=require"
	secrets := snapshotSecrets{
		resourceKey("org_1", "env_1", "db", ResourceURL):    []byte(doDSN),
		resourceKey("org_1", "env_1", "cache", ResourceURL): []byte("redis://:pw@cache:6379"),
	}
	services := map[string]*planning.Binding{"db-gateway": gatewayBinding(t, "https://db-gateway-xyz.run.app")}
	resources := map[string]*planning.Binding{
		"db": {Subject: planning.ResourceSubject, Name: "db"}, "cache": {Subject: planning.ResourceSubject, Name: "cache"},
	}
	links := []planning.EnvLink{
		gatewayLink,
		{Key: "REDIS_URL", From: "cache", Subject: planning.ResourceSubject},
	}
	check := func(t *testing.T, env map[string]string, secret map[string]bool, direct string, directSecret bool) {
		t.Helper()
		if env["DATABASE_URL"] != gatewayDSN || !secret["DATABASE_URL"] ||
			env["DATABASE_URL_GATEWAY"] != "https://db-gateway-xyz.run.app" || secret["DATABASE_URL_GATEWAY"] ||
			env["DATABASE_URL_DIRECT"] != direct || secret["DATABASE_URL_DIRECT"] != directSecret {
			t.Fatalf("served link: env %v secret %v", env, secret)
		}
		if _, ok := env["REDIS_URL_GATEWAY"]; ok {
			t.Fatalf("an unserved link got a gateway address: %v", env)
		}
		if _, ok := env["REDIS_URL_DIRECT"]; ok {
			t.Fatalf("an unserved link got a direct address: %v", env)
		}
		if env["REDIS_URL"] != "redis://:pw@cache:6379" {
			t.Fatalf("unserved link: %v", env)
		}
	}

	// Without a snapshot, read afresh.
	r := NewRunner(RunnerDeps{Variables: fakeVariables{"svc": {}}, Resources: secrets})
	plain := planning.Action{ID: "service:plain", Name: "plain", Service: &planning.ServiceConfig{Service: "svc", Env: links}}
	env, secret, err := r.env(ctx, gatewayDeployment, plain, services, resources)
	if err != nil {
		t.Fatal(err)
	}
	check(t, env, secret, doDSN, true)

	// Through the applied-inputs snapshot, which captures what the app gets.
	a := planning.Action{ID: "service:web", Name: "web", Service: &planning.ServiceConfig{Service: "svc", Env: links}}
	if err = r.captureAppliedVariables(ctx, gatewayDeployment, &a, services); err != nil {
		t.Fatal(err)
	}
	if env, secret, err = r.env(ctx, gatewayDeployment, a, services, resources); err != nil {
		t.Fatal(err)
	}
	check(t, env, secret, doDSN, true)
	var captured appliedVariables
	if err = json.Unmarshal(secrets[a.Service.AppliedInputs.Variables], &captured); err != nil {
		t.Fatal(err)
	}
	if captured.ResourceValues["DATABASE_URL"] != env["DATABASE_URL"] || captured.GatewayValues["DATABASE_URL_GATEWAY"] != env["DATABASE_URL_GATEWAY"] ||
		captured.GatewayValues["DATABASE_URL_DIRECT"] != env["DATABASE_URL_DIRECT"] || !captured.GatewaySecrets["DATABASE_URL_DIRECT"] ||
		captured.GatewaySecrets["DATABASE_URL_GATEWAY"] || len(captured.GatewayValues) != 2 {
		t.Fatalf("captured %+v, the app got %v", captured, env)
	}
	// A retry from the snapshot hands out the same, with the same secrecy.
	retry := planning.Action{ID: a.ID, Name: "web", Service: a.Service}
	again, againSecret, err := r.env(ctx, gatewayDeployment, retry, services, resources)
	if err != nil {
		t.Fatal(err)
	}
	check(t, again, againSecret, doDSN, true)

	// A variable someone set of that name wins, with the secrecy they gave it.
	r.variables = fakeVariables{"svc": {"DATABASE_URL_DIRECT": "postgres://mine"}}
	for _, snapshot := range []bool{false, true} {
		b := planning.Action{ID: "service:own", Name: "own", Service: &planning.ServiceConfig{Service: "svc", Env: links}}
		if snapshot {
			b.ID = "service:own-pinned"
			if err = r.captureAppliedVariables(ctx, gatewayDeployment, &b, services); err != nil {
				t.Fatal(err)
			}
		}
		if env, secret, err = r.env(ctx, gatewayDeployment, b, services, resources); err != nil {
			t.Fatal(err)
		}
		check(t, env, secret, "postgres://mine", false)
	}
}

// optOutRun deploys a plan in which db opts out of a running gateway that
// serves db and orders, with api's link to db planned direct, and returns
// what api was deployed with. gateway is the plan's db-gateway action.
func optOutRun(t *testing.T, gateway planning.Action) *Job {
	t.Helper()
	url, client := tlsAnswering(t)
	secrets := snapshotSecrets{resourceKey("org_1", "env_1", "db", ResourceURL): []byte(doDSN)}
	gw, _, _ := mintGateway(t, secrets, dbgateway.Gateway{OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Name: "db-gateway",
		Provider: "cloud-run", Region: "us-central1", Suffix: "us-central1.db.example.com"})
	store := &fakeDBGateways{gateways: []dbgateway.Gateway{gw}, routes: map[string][]string{gw.ID: {"db", "orders"}}}
	served := dbGatewayAction().Service
	served.DatabaseGateway.Resources = []string{"db", "orders"}
	cfg, err := json.Marshal(served)
	if err != nil {
		t.Fatal(err)
	}
	plan := &planning.Plan{
		ID: "plan_out", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Environment: "production",
		Status: planning.StatusApproved, Executable: true, Connections: map[string]string{"gcp": "conn_g"},
		Actions: []planning.Action{{
			ID: "service:api", Op: planning.Update, Subject: planning.ServiceSubject, Name: "api", Provider: "cloud-run", Step: 1,
			Service: &planning.ServiceConfig{Provider: "cloud-run", Service: "svc:go:api", Public: true,
				Env: []planning.EnvLink{{Key: "DATABASE_URL", From: "db", Subject: planning.ResourceSubject}}},
		}, gateway},
	}
	plans := &fakePlans{plan: plan, bindings: map[string]*planning.Binding{
		"resource:db": {ID: "bind_db", Subject: planning.ResourceSubject, Name: "db", Provider: "digitalocean"},
		"service:db-gateway": {
			ID: "bind_gw", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Subject: planning.ServiceSubject, Name: "db-gateway",
			Provider: "cloud-run", Region: "us-central1", ConnectionID: "conn_g", ExternalID: "ext-db-gateway", URL: url + "/db-gateway", Config: cfg,
		},
	}}
	exec := &removing{recorder: &recorder{url: url}, gateways: store}
	r := NewRunner(RunnerDeps{
		Store: &memStore{d: map[string]*Deployment{}}, Plans: plans, Credentials: fakeCredentials{}, Variables: fakeVariables{},
		Sources: &fakeSources{}, Providers: available(t), Executors: map[string]Executor{"cloud-run": exec}, Resources: secrets,
		DatabaseGateways: store, PublicURL: "https://loomplane.example.com",
		DatabaseGatewayTrust: func() ([]plane.TrustKey, error) { return []plane.TrustKey{testTrustKey(t)}, nil },
	})
	r.http, r.verifyWait = client, time.Second
	if _, err = runAll(t, r, plan); err != nil {
		t.Fatal(err)
	}
	for _, job := range exec.jobs {
		if job.Action.Name != "api" {
			continue
		}
		if job.Action.Service.AppliedInputs == nil {
			t.Fatal("api's variables weren't captured")
		}
		var captured appliedVariables
		if err = json.Unmarshal(secrets[job.Action.Service.AppliedInputs.Variables], &captured); err != nil {
			t.Fatal(err)
		}
		if captured.ResourceValues["DATABASE_URL"] != doDSN || len(captured.GatewayValues) != 0 {
			t.Fatalf("captured %+v, want the direct address alone", captured)
		}
		return job
	}
	t.Fatal("api wasn't deployed")
	return nil
}

func wantDirect(t *testing.T, job *Job) {
	t.Helper()
	if job.Env["DATABASE_URL"] != doDSN || !job.Secrets["DATABASE_URL"] {
		t.Fatalf("api env %v, want the direct address", job.Env)
	}
	for _, k := range []string{"DATABASE_URL_GATEWAY", "DATABASE_URL_DIRECT"} {
		if _, ok := job.Env[k]; ok {
			t.Fatalf("api got %s after opting out: %v", k, job.Env)
		}
	}
}

// Turning the gateway off updates the app before the gateway is deleted,
// and the app gets the database's own address, not the gateway it's about
// to lose.
func TestRunFullOptOutPointsAppsDirect(t *testing.T) {
	gone := planning.Action{ID: "service:db-gateway", Op: planning.Delete, Subject: planning.ServiceSubject,
		Name: "db-gateway", Provider: "cloud-run", Region: "us-central1", ConnectionID: "conn_g", Step: 2}
	wantDirect(t, optOutRun(t, gone))
}

// Taking one database off the gateway gets the app its own address even
// when the app deploys before the gateway drops the route.
func TestRunPartialOptOutPointsAppsDirect(t *testing.T) {
	kept := dbGatewayAction()
	kept.Op, kept.Step = planning.Update, 2
	kept.Service.DatabaseGateway.Resources = []string{"orders"}
	wantDirect(t, optOutRun(t, kept))
}

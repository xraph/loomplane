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
	"fmt"
	neturl "net/url"
	"slices"
	"strings"

	"github.com/xraph/loomplane/internal/dbgateway"
	"github.com/xraph/loomplane/internal/dbgateway/plane"
	"github.com/xraph/loomplane/internal/planning"
)

// GatewaySuffix ends the variable that carries the database gateway's
// address, beside the one that carries a resource's: DATABASE_URL gets
// DATABASE_URL_GATEWAY, for a driver that reaches Postgres over HTTP.
const GatewaySuffix = "_GATEWAY"

// The fields a database gateway's private keys are kept under in Vault.
const (
	dbGatewaySigningField = "signing_key"
	dbGatewaySealingField = "sealing_key"
)

// dbGatewayPort is the port an app reaches Postgres on through the gateway.
const dbGatewayPort = "5432"

// errDBGatewayKeysMissing is a recorded gateway whose private keys aren't in
// Vault. New keys would not match the ones the gateway is recorded with, so
// a deploy never mints them for a gateway that exists.
var errDBGatewayKeysMissing = errors.New("the database gateway's keys are missing; remove the gateway and deploy again")

// DatabaseGatewayStore records each environment's database gateway and its
// routes. *dbgateway.Store is the real one.
type DatabaseGatewayStore interface {
	Gateways(ctx context.Context, org, environmentID string) ([]dbgateway.Gateway, error)
	EnsureGateway(ctx context.Context, g dbgateway.Gateway) (dbgateway.Gateway, bool, error)
	EnsureRoute(ctx context.Context, org, gatewayID, resource string) (dbgateway.RouteRecord, error)
}

var _ DatabaseGatewayStore = (*dbgateway.Store)(nil)

// dbGatewayKey is where one of a gateway's private keys is kept: under the
// gateway's id, so keys minted for an id that never got recorded can't be
// mistaken for another gateway's.
func dbGatewayKey(org, environmentID, gatewayID, field string) string {
	return resourceKey(org, environmentID, "db-gateway/"+gatewayID, field)
}

// dbGatewayKeys are a gateway's private keys.
type dbGatewayKeys struct {
	signing ed25519.PrivateKey
	sealing *ecdh.PrivateKey
}

// signingPublic is the public half of the signing key, which an Ed25519
// private key carries after its seed.
func (k dbGatewayKeys) signingPublic() ed25519.PublicKey {
	return bytes.Clone(k.signing[ed25519.SeedSize:])
}

// ensureDatabaseGateway records the database gateway the action deploys,
// minting its keys the first time, ensures a route for each resource it
// serves, and returns the gateway's environment. The private keys are
// secret, and nothing else in it is.
func (r *Runner) ensureDatabaseGateway(ctx context.Context, d *Deployment, a *planning.Action) (env map[string]string, secret map[string]bool, err error) {
	cfg := a.Service.DatabaseGateway
	if r.dbGateways == nil || r.resources == nil {
		return nil, nil, errors.New("this Loomplane has no store for database gateways, so it can't deploy one")
	}
	var trust []plane.TrustKey
	if r.dbTrust != nil {
		trust = r.dbTrust()
	}
	if len(trust) == 0 {
		return nil, nil, dbgateway.ErrNotConfigured
	}
	control, err := dbGatewayControlURL(r.publicURL)
	if err != nil {
		return nil, nil, err
	}
	trustJSON, err := json.Marshal(trust)
	if err != nil {
		return nil, nil, fmt.Errorf("encode the database gateway's trust keys: %w", err)
	}
	gw, keys, err := r.databaseGatewayIdentity(ctx, d, a)
	if err != nil {
		return nil, nil, err
	}
	for _, resource := range cfg.Resources {
		if _, err = r.dbGateways.EnsureRoute(ctx, d.OrgID, gw.ID, resource); err != nil {
			return nil, nil, fmt.Errorf("record the database gateway's route to %s: %w", resource, err)
		}
	}
	env = map[string]string{
		"LOOMPLANE_DBGW_ID":          gw.ID,
		"LOOMPLANE_DBGW_CONTROL_URL": control,
		"LOOMPLANE_DBGW_SUFFIX":      cfg.Suffix,
		"LOOMPLANE_DBGW_TRUST_KEYS":  string(trustJSON),
		"LOOMPLANE_DBGW_SIGNING_KEY": base64.StdEncoding.EncodeToString(keys.signing.Seed()),
		"LOOMPLANE_DBGW_SEALING_KEY": base64.StdEncoding.EncodeToString(keys.sealing.Bytes()),
	}
	// The gateway refuses a configuration it can't run with, so one that
	// wouldn't start fails here, before anything deploys. Its errors name
	// the variable and never repeat a key.
	if _, err = plane.ConfigFromEnv(func(k string) string { return env[k] }); err != nil {
		return nil, nil, fmt.Errorf("the database gateway's configuration is invalid: %w", err)
	}
	return env, map[string]bool{"LOOMPLANE_DBGW_SIGNING_KEY": true, "LOOMPLANE_DBGW_SEALING_KEY": true}, nil
}

// databaseGatewayIdentity returns the environment's recorded gateway of the
// action's name with its private keys, recording one first if there's none.
//
// A new gateway's keys go into Vault under a fresh id before the gateway is
// recorded with that id, so a crash between the two leaves only keys nobody
// uses, never a gateway without keys. A deploy that loses the race to record
// the gateway drops its own keys and takes the winner's.
func (r *Runner) databaseGatewayIdentity(ctx context.Context, d *Deployment, a *planning.Action) (dbgateway.Gateway, dbGatewayKeys, error) {
	cfg := a.Service.DatabaseGateway
	recorded, err := r.dbGateways.Gateways(ctx, d.OrgID, d.EnvironmentID)
	if err != nil {
		return dbgateway.Gateway{}, dbGatewayKeys{}, fmt.Errorf("read the database gateway: %w", err)
	}
	if i := slices.IndexFunc(recorded, func(g dbgateway.Gateway) bool { return g.Name == cfg.Name }); i >= 0 {
		keys, kerr := r.databaseGatewayKeys(ctx, d, recorded[i])
		return recorded[i], keys, kerr
	}
	id, err := dbgateway.NewGatewayID()
	if err != nil {
		return dbgateway.Gateway{}, dbGatewayKeys{}, err
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err = rand.Read(seed); err != nil {
		return dbgateway.Gateway{}, dbGatewayKeys{}, fmt.Errorf("mint the database gateway's signing key: %w", err)
	}
	keys := dbGatewayKeys{signing: ed25519.NewKeyFromSeed(seed)}
	clear(seed)
	if keys.sealing, err = ecdh.X25519().GenerateKey(rand.Reader); err != nil {
		return dbgateway.Gateway{}, dbGatewayKeys{}, fmt.Errorf("mint the database gateway's sealing key: %w", err)
	}
	signingKey, sealingKey := dbGatewayKey(d.OrgID, d.EnvironmentID, id, dbGatewaySigningField), dbGatewayKey(d.OrgID, d.EnvironmentID, id, dbGatewaySealingField)
	if err = r.resources.Put(ctx, signingKey, keys.signing.Seed()); err != nil {
		return dbgateway.Gateway{}, dbGatewayKeys{}, fmt.Errorf("keep the database gateway's signing key: %w", err)
	}
	if err = r.resources.Put(ctx, sealingKey, keys.sealing.Bytes()); err != nil {
		return dbgateway.Gateway{}, dbGatewayKeys{}, fmt.Errorf("keep the database gateway's sealing key: %w", err)
	}
	stored, created, err := r.dbGateways.EnsureGateway(ctx, dbgateway.Gateway{
		OrgID: d.OrgID, ID: id, ProjectID: d.ProjectID, EnvironmentID: d.EnvironmentID, Name: cfg.Name,
		Provider: a.Provider, Region: a.Region, Suffix: cfg.Suffix,
		SigningPublicKey: keys.signingPublic(), SealingPublicKey: keys.sealing.PublicKey(),
	})
	if err != nil {
		// The keys stay: the gateway may have been recorded with them
		// before the error, and a retry finds it and needs them.
		return dbgateway.Gateway{}, dbGatewayKeys{}, fmt.Errorf("record the database gateway: %w", err)
	}
	// A store that recorded this gateway but couldn't say so returns it as
	// though another deploy had: its id tells.
	if created || stored.ID == id {
		return stored, keys, nil
	}
	// Another deploy recorded the gateway first, so these keys were never
	// used. Dropping them is best effort: a stray copy only costs storage.
	_ = r.resources.Delete(ctx, signingKey) //nolint:errcheck // best effort, see above
	_ = r.resources.Delete(ctx, sealingKey) //nolint:errcheck // best effort, see above
	winner, err := r.databaseGatewayKeys(ctx, d, stored)
	return stored, winner, err
}

// databaseGatewayKeys reads a recorded gateway's private keys from Vault and
// checks they're the ones it was recorded with.
func (r *Runner) databaseGatewayKeys(ctx context.Context, d *Deployment, gw dbgateway.Gateway) (dbGatewayKeys, error) {
	read := func(field string) ([]byte, error) {
		v, err := r.resources.Get(ctx, dbGatewayKey(d.OrgID, d.EnvironmentID, gw.ID, field))
		switch {
		case errors.Is(err, ErrNoCredential):
			return nil, errDBGatewayKeysMissing
		case err != nil:
			return nil, fmt.Errorf("read the database gateway's keys: %w", err)
		}
		return v, nil
	}
	seed, err := read(dbGatewaySigningField)
	if err != nil {
		return dbGatewayKeys{}, err
	}
	defer clear(seed)
	raw, err := read(dbGatewaySealingField)
	if err != nil {
		return dbGatewayKeys{}, err
	}
	defer clear(raw)
	if len(seed) != ed25519.SeedSize {
		return dbGatewayKeys{}, errDBGatewayKeysMissing
	}
	keys := dbGatewayKeys{signing: ed25519.NewKeyFromSeed(seed)}
	if keys.sealing, err = ecdh.X25519().NewPrivateKey(raw); err != nil {
		return dbGatewayKeys{}, errDBGatewayKeysMissing
	}
	if !bytes.Equal(keys.signingPublic(), gw.SigningPublicKey) ||
		gw.SealingPublicKey == nil || !bytes.Equal(keys.sealing.PublicKey().Bytes(), gw.SealingPublicKey.Bytes()) {
		return dbGatewayKeys{}, errors.New("the database gateway's keys don't match the ones it was recorded with; remove the gateway and deploy again")
	}
	return keys, nil
}

// dbGatewayControlURL is the address a database gateway polls Loomplane at:
// public_url, which has to be https unless it's on this machine, since the
// gateway refuses anything else.
func dbGatewayControlURL(public string) (string, error) {
	u, err := neturl.Parse(public)
	ok := err == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
	if ok && u.Scheme == "http" {
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
		default:
			ok = false
		}
	} else if ok && u.Scheme != "https" {
		ok = false
	}
	if !ok {
		// The value isn't repeated: a URL can carry a password.
		return "", errors.New("a database gateway reaches Loomplane at public_url, which must be an https URL with no credentials, query or fragment (http only on localhost)")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// resourceAddress is what app gets in key for resource: the address kept
// for it, or, when the environment's database gateway serves it, that
// address pointed at the gateway, with the gateway's own address in
// key+GatewaySuffix. services are the environment's service bindings. The
// address holds the resource's credentials, so no error repeats it.
func (r *Runner) resourceAddress(ctx context.Context, d *Deployment, app string, services map[string]*planning.Binding, resource, key string) (value string, extra map[string]string, err error) {
	if r.resources == nil {
		return "", nil, fmt.Errorf("%s needs %s, which hasn't been provisioned", app, resource)
	}
	raw, err := r.resources.Get(ctx, resourceKey(d.OrgID, d.EnvironmentID, resource, ResourceURL))
	if err != nil {
		return "", nil, fmt.Errorf("%s needs %s: %w", app, resource, err)
	}
	gw, suffix := servingGateway(services, resource)
	if gw == nil {
		return string(raw), nil, nil
	}
	if gw.URL == "" {
		return "", nil, fmt.Errorf("%s needs the database gateway, which has no address yet", app)
	}
	if u, perr := neturl.Parse(gw.URL); perr != nil || u.Scheme != "https" || u.Host == "" {
		return "", nil, fmt.Errorf("%s needs the database gateway, whose address %s isn't https", app, gw.URL)
	}
	u, err := neturl.Parse(string(raw))
	if err != nil || u.Host == "" {
		return "", nil, fmt.Errorf("%s needs %s, whose address isn't a URL the database gateway can stand in for", app, resource)
	}
	u.Host = resource + "." + suffix + ":" + dbGatewayPort
	return u.String(), map[string]string{key + GatewaySuffix: gw.URL}, nil
}

// servingGateway returns the database gateway binding that serves resource,
// and the suffix its hosts are named under, or nil.
func servingGateway(services map[string]*planning.Binding, resource string) (gw *planning.Binding, suffix string) {
	for _, b := range services {
		var cfg planning.ServiceConfig
		if len(b.Config) == 0 || json.Unmarshal(b.Config, &cfg) != nil || cfg.DatabaseGateway == nil {
			continue
		}
		if slices.Contains(cfg.DatabaseGateway.Resources, resource) {
			return b, cfg.DatabaseGateway.Suffix
		}
	}
	return nil, ""
}

// setUnset sets k to v unless env has it already, as a variable someone set
// wins over an address Loomplane would set.
func setUnset(env map[string]string, k, v string) {
	if _, set := env[k]; !set {
		env[k] = v
	}
}

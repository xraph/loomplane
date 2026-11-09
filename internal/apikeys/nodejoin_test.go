package apikeys

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xraph/keysmith"
	"github.com/xraph/keysmith/id"
	"github.com/xraph/keysmith/store/memory"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

type joinAuthz struct{}

// admin and platform may manage nodes; dev may not.
func (joinAuthz) Allowed(_ context.Context, p security.Principal, perm security.Permission) (bool, error) {
	if p.ReadOnly || perm != security.NodeManage {
		return false, nil
	}
	return p.UserID == "admin" || p.UserID == "platform", nil
}

type joinPlatform struct{}

func (joinPlatform) IsPlatformAdmin(_ context.Context, userID string) (bool, error) {
	return userID == "platform", nil
}

// liveAuthz lets a test change who may manage nodes after a token is minted.
type liveAuthz struct {
	mu       sync.Mutex
	demoted  map[string]bool
	platform map[string]bool
}

func newLiveAuthz() *liveAuthz {
	return &liveAuthz{demoted: map[string]bool{}, platform: map[string]bool{"platform": true}}
}

func (l *liveAuthz) Allowed(ctx context.Context, p security.Principal, perm security.Permission) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.demoted[p.UserID] {
		return false, nil
	}
	return joinAuthz{}.Allowed(ctx, p, perm)
}

func (l *liveAuthz) IsPlatformAdmin(_ context.Context, userID string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.platform[userID], nil
}

func liveService(t *testing.T) (*Service, *liveAuthz) {
	t.Helper()
	engine, err := keysmith.NewEngine(keysmith.WithStore(memory.New()))
	if err != nil {
		t.Fatal(err)
	}
	l := newLiveAuthz()
	return New(engine, "app", l, nil, WithPlatformAdmins(l)), l
}

func mustKeyID(t *testing.T, s string) id.KeyID {
	t.Helper()
	kid, err := id.ParseKeyID(s)
	if err != nil {
		t.Fatal(err)
	}
	return kid
}

func assertBurned(t *testing.T, svc *Service, secret string) {
	t.Helper()
	if _, err := svc.ConsumeNodeJoinToken(t.Context(), secret); !errors.Is(err, ErrRejected) {
		t.Fatalf("token usable after refusal: %v", err)
	}
	res, err := svc.engine.ValidateKey(t.Context(), secret)
	if err == nil {
		t.Fatalf("token still validates: %+v", res)
	}
}

type joinAudit struct {
	mu      sync.Mutex
	actions []string
	metas   []map[string]string
}

func (a *joinAudit) RecordWithMeta(ctx context.Context, action string, p security.Principal, k APIKey, meta map[string]string) {
	a.mu.Lock()
	a.metas = append(a.metas, meta)
	a.mu.Unlock()
	a.Record(ctx, action, p, k)
}

func (a *joinAudit) Record(_ context.Context, action string, _ security.Principal, _ APIKey) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.actions = append(a.actions, action)
}

func joinService(t *testing.T, audit Auditor) *Service {
	t.Helper()
	engine, err := keysmith.NewEngine(keysmith.WithStore(memory.New()))
	if err != nil {
		t.Fatal(err)
	}
	return New(engine, "app", joinAuthz{}, audit, WithPlatformAdmins(joinPlatform{}))
}

func TestJoinTokenFromAdminKey(t *testing.T) {
	audit := &joinAudit{}
	svc := joinService(t, audit)
	mk, err := svc.Create(t.Context(), security.Principal{UserID: "admin", OrgID: "org"}, NewKey{Name: "ci", Access: Write})
	if err != nil {
		t.Fatal(err)
	}
	p := security.Principal{UserID: "admin", OrgID: "org", KeyID: mk.Key.ID}
	scope := NodeJoinScope{CloudID: "cloud", Labels: map[string]string{"zone": "a"}, Endpoints: []nodeproto.Endpoint{nodeproto.EndpointProbe, nodeproto.EndpointDocker}, Edge: true, EdgeScope: EdgeScopeOrg}
	tok, err := svc.CreateNodeJoinToken(t.Context(), p, scope, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !IsNodeJoinToken(tok.Secret) || IsKey(tok.Secret) || tok.ID == "" {
		t.Fatalf("wrong token shape: %q", tok.Secret)
	}
	claim, err := svc.ConsumeNodeJoinToken(t.Context(), tok.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if claim.OrgID != "org" || claim.MintedBy != "admin" || claim.MintedByKeyID != mk.Key.ID {
		t.Fatalf("wrong claim: %+v", claim)
	}
	if claim.Scope.CloudID != "cloud" || claim.Scope.Labels["zone"] != "a" || len(claim.Scope.Endpoints) != 2 || claim.Scope.Endpoints[1] != nodeproto.EndpointDocker || !claim.Scope.Edge || claim.Scope.EdgeScope != EdgeScopeOrg {
		t.Fatalf("scope lost: %+v", claim.Scope)
	}
	if len(audit.metas) != 1 || audit.metas[0]["minted_by_key_id"] != mk.Key.ID || audit.metas[0]["edge_scope"] != EdgeScopeOrg {
		t.Fatalf("audit meta: %v", audit.metas)
	}
	if len(audit.actions) != 2 || audit.actions[1] != "node.join_token.created" {
		t.Fatalf("audit: %v", audit.actions)
	}
	if _, err = svc.ConsumeNodeJoinToken(t.Context(), tok.Secret); !errors.Is(err, ErrRejected) {
		t.Fatalf("token spent twice: %v", err)
	}
}

func TestJoinTokenFromSessionHasNoKeyID(t *testing.T) {
	svc := joinService(t, nil)
	tok, err := svc.CreateNodeJoinToken(t.Context(), security.Principal{UserID: "admin", OrgID: "org"}, NodeJoinScope{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := svc.ConsumeNodeJoinToken(t.Context(), tok.Secret)
	if err != nil || claim.MintedByKeyID != "" {
		t.Fatalf("claim %+v err %v", claim, err)
	}
}

func TestJoinTokenRefusesReadKey(t *testing.T) {
	svc := joinService(t, nil)
	p := security.Principal{UserID: "admin", OrgID: "org", KeyID: "key-1", ReadOnly: true}
	if _, err := svc.CreateNodeJoinToken(t.Context(), p, NodeJoinScope{}, time.Minute); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("read key minted a token: %v", err)
	}
}

func TestJoinTokenRefusesDeveloper(t *testing.T) {
	svc := joinService(t, nil)
	if _, err := svc.CreateNodeJoinToken(t.Context(), security.Principal{UserID: "dev", OrgID: "org"}, NodeJoinScope{}, time.Minute); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("developer minted a token: %v", err)
	}
}

func TestJoinTokenRefusesFleetAndSystem(t *testing.T) {
	svc := joinService(t, nil)
	fleet := security.Principal{UserID: "admin", OrgID: "org", Kind: security.KindFleet, Fleet: &security.FleetScope{ProjectID: "p", FleetID: "f"}}
	system := security.SystemPrincipal("org")
	for _, p := range []security.Principal{fleet, system} {
		if _, err := svc.CreateNodeJoinToken(t.Context(), p, NodeJoinScope{}, time.Minute); !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("principal %+v minted a token: %v", p, err)
		}
	}
}

func TestJoinTokenLifetimeClamped(t *testing.T) {
	svc := joinService(t, nil)
	p := security.Principal{UserID: "admin", OrgID: "org"}
	for _, ask := range []time.Duration{3 * time.Hour, 0, -time.Minute} {
		before := time.Now()
		tok, err := svc.CreateNodeJoinToken(t.Context(), p, NodeJoinScope{}, ask)
		if err != nil {
			t.Fatal(err)
		}
		if tok.ExpiresAt.After(time.Now().Add(time.Hour)) || tok.ExpiresAt.Before(before.Add(59*time.Minute)) {
			t.Fatalf("asked %v, expires %v", ask, tok.ExpiresAt)
		}
	}
	short, err := svc.CreateNodeJoinToken(t.Context(), p, NodeJoinScope{}, 5*time.Minute)
	if err != nil || short.ExpiresAt.After(time.Now().Add(6*time.Minute)) {
		t.Fatalf("short lifetime not kept: %v %v", short, err)
	}
}

func TestJoinTokenExpired(t *testing.T) {
	svc := joinService(t, nil)
	tok, err := svc.CreateNodeJoinToken(t.Context(), security.Principal{UserID: "admin", OrgID: "org"}, NodeJoinScope{}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err = svc.ConsumeNodeJoinToken(t.Context(), tok.Secret); !errors.Is(err, ErrRejected) {
		t.Fatalf("expired token consumed: %v", err)
	}
}

func TestJoinTokenSingleUseConcurrent(t *testing.T) {
	svc := joinService(t, nil)
	for round := 0; round < 25; round++ {
		tok, err := svc.CreateNodeJoinToken(t.Context(), security.Principal{UserID: "admin", OrgID: "org"}, NodeJoinScope{CloudID: "c"}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		const callers = 8
		var wg sync.WaitGroup
		start := make(chan struct{})
		results := make(chan error, callers)
		for i := 0; i < callers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, cerr := svc.ConsumeNodeJoinToken(t.Context(), tok.Secret)
				results <- cerr
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		wins := 0
		for cerr := range results {
			switch {
			case cerr == nil:
				wins++
			case !errors.Is(cerr, ErrRejected):
				t.Fatalf("loser got %v, want ErrRejected", cerr)
			}
		}
		if wins != 1 {
			t.Fatalf("round %d: %d callers won the token, want exactly 1", round, wins)
		}
	}
}

func TestJoinTokenIsNotAnAPIKey(t *testing.T) {
	svc := joinService(t, nil)
	p := security.Principal{UserID: "admin", OrgID: "org"}
	tok, err := svc.CreateNodeJoinToken(t.Context(), p, NodeJoinScope{}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Authenticate(t.Context(), tok.Secret); !errors.Is(err, ErrRejected) {
		t.Fatalf("join token authenticated as an API key: %v", err)
	}
	// Keysmith hashes the whole raw key, so the kind check is tested directly.
	stored, err := svc.engine.GetKey(svc.tenant(t.Context(), "org"), mustKeyID(t, tok.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.fleetPrincipal(stored); !errors.Is(err, ErrRejected) {
		t.Fatalf("join token key became a fleet principal: %v", err)
	}
	saved := security.Principal{UserID: "admin", OrgID: "org", KeyID: tok.ID}
	if err = svc.ValidatePrincipal(t.Context(), saved); !errors.Is(err, ErrRejected) {
		t.Fatalf("join token accepted as saved key authority: %v", err)
	}
	// An API key is no join token, and trying to spend one leaves it alive.
	created, err := svc.Create(t.Context(), p, NewKey{Name: "ci", Access: Write})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ConsumeNodeJoinToken(t.Context(), created.Secret); !errors.Is(err, ErrRejected) {
		t.Fatalf("API key consumed as a join token: %v", err)
	}
	if _, err = svc.Authenticate(t.Context(), created.Secret); err != nil {
		t.Fatalf("API key harmed by a consume attempt: %v", err)
	}
}

func TestInstanceEdgeNeedsPlatformAdmin(t *testing.T) {
	svc := joinService(t, nil)
	scope := NodeJoinScope{Edge: true, EdgeScope: EdgeScopeInstance}
	if _, err := svc.CreateNodeJoinToken(t.Context(), security.Principal{UserID: "admin", OrgID: "org"}, scope, time.Minute); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("org admin minted an instance edge: %v", err)
	}
	tok, err := svc.CreateNodeJoinToken(t.Context(), security.Principal{UserID: "platform", OrgID: "org"}, scope, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claim, cerr := svc.ConsumeNodeJoinToken(t.Context(), tok.Secret); cerr != nil || claim.Scope.EdgeScope != EdgeScopeInstance {
		t.Fatalf("claim %+v err %v", claim, cerr)
	}
	// With no PlatformAdmins wired, nobody is one.
	engine, _ := keysmith.NewEngine(keysmith.WithStore(memory.New()))
	bare := New(engine, "app", joinAuthz{}, nil)
	if _, err = bare.CreateNodeJoinToken(t.Context(), security.Principal{UserID: "platform", OrgID: "org"}, scope, time.Minute); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("instance edge minted without a platform admin check: %v", err)
	}
}

func TestJoinTokenBadScope(t *testing.T) {
	svc := joinService(t, nil)
	p := security.Principal{UserID: "admin", OrgID: "org"}
	for _, sc := range []NodeJoinScope{{EdgeScope: "galaxy", Edge: true}, {EdgeScope: EdgeScopeOrg}, {Labels: map[string]string{"": "x"}}, {Labels: map[string]string{"build_workers": "shared"}}, {Labels: map[string]string{"build_workers": ""}}} {
		if _, err := svc.CreateNodeJoinToken(t.Context(), p, sc, time.Minute); !errors.Is(err, ErrInvalid) {
			t.Fatalf("scope %+v: %v", sc, err)
		}
	}
}

// node join --builder dedicated mints build_workers=dedicated into the
// scope, which is the one value a scope may carry for it.
func TestJoinTokenDedicatedBuilder(t *testing.T) {
	svc := joinService(t, nil)
	p := security.Principal{UserID: "admin", OrgID: "org"}
	if _, err := svc.CreateNodeJoinToken(t.Context(), p, NodeJoinScope{Labels: map[string]string{"build_workers": "dedicated"}}, time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestConsumeRefusesDemotedMinter(t *testing.T) {
	svc, l := liveService(t)
	tok, err := svc.CreateNodeJoinToken(t.Context(), security.Principal{UserID: "admin", OrgID: "org"}, NodeJoinScope{}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	l.demoted["admin"] = true
	l.mu.Unlock()
	if _, err = svc.ConsumeNodeJoinToken(t.Context(), tok.Secret); !errors.Is(err, ErrRejected) {
		t.Fatalf("demoted minter's token consumed: %v", err)
	}
	assertBurned(t, svc, tok.Secret)
}

func TestConsumeRefusesRevokedMintingKey(t *testing.T) {
	svc, _ := liveService(t)
	owner := security.Principal{UserID: "admin", OrgID: "org"}
	mk, err := svc.Create(t.Context(), owner, NewKey{Name: "ci", Access: Write})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := svc.CreateNodeJoinToken(t.Context(), security.Principal{UserID: "admin", OrgID: "org", KeyID: mk.Key.ID}, NodeJoinScope{}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Revoke(t.Context(), owner, mk.Key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ConsumeNodeJoinToken(t.Context(), tok.Secret); !errors.Is(err, ErrRejected) {
		t.Fatalf("token of a revoked key consumed: %v", err)
	}
	assertBurned(t, svc, tok.Secret)
}

func TestConsumeRefusesInstanceEdgeAfterPlatformAdminRemoved(t *testing.T) {
	svc, l := liveService(t)
	tok, err := svc.CreateNodeJoinToken(t.Context(), security.Principal{UserID: "platform", OrgID: "org"}, NodeJoinScope{Edge: true, EdgeScope: EdgeScopeInstance}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	l.platform["platform"] = false
	l.mu.Unlock()
	if _, err = svc.ConsumeNodeJoinToken(t.Context(), tok.Secret); !errors.Is(err, ErrRejected) {
		t.Fatalf("instance token consumed after demotion: %v", err)
	}
	assertBurned(t, svc, tok.Secret)
}

func TestInstanceEdgeRefusesAPIKey(t *testing.T) {
	svc := joinService(t, nil)
	p := security.Principal{UserID: "platform", OrgID: "org", KeyID: "key-1"}
	if _, err := svc.CreateNodeJoinToken(t.Context(), p, NodeJoinScope{Edge: true, EdgeScope: EdgeScopeInstance}, time.Minute); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("API key minted an instance edge: %v", err)
	}
}

func TestJoinTokenEndpoints(t *testing.T) {
	svc := joinService(t, nil)
	p := security.Principal{UserID: "admin", OrgID: "org"}
	for _, bad := range []nodeproto.Endpoint{nodeproto.EndpointControl, nodeproto.EndpointPublic, "bogus"} {
		if _, err := svc.CreateNodeJoinToken(t.Context(), p, NodeJoinScope{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker, bad}}, time.Minute); !errors.Is(err, ErrInvalid) {
			t.Fatalf("endpoint %q: %v", bad, err)
		}
	}
	tok, err := svc.CreateNodeJoinToken(t.Context(), p, NodeJoinScope{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker, nodeproto.EndpointKube, nodeproto.EndpointDocker}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := svc.ConsumeNodeJoinToken(t.Context(), tok.Secret)
	if err != nil || len(claim.Scope.Endpoints) != 2 {
		t.Fatalf("duplicates kept: %+v %v", claim, err)
	}
}

func TestListLeavesOutJoinTokens(t *testing.T) {
	svc := joinService(t, nil)
	p := security.Principal{UserID: "admin", OrgID: "org"}
	if _, err := svc.CreateNodeJoinToken(t.Context(), p, NodeJoinScope{}, time.Minute); err != nil {
		t.Fatal(err)
	}
	// joinAuthz denies OrganizationAdminister, so List shows only the caller's own keys.
	if _, err := svc.Create(t.Context(), p, NewKey{Name: "ci", Access: Write}); err != nil {
		t.Fatal(err)
	}
	keys, err := svc.List(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Name != "ci" {
		t.Fatalf("join token listed: %+v", keys)
	}
}

// A peek reads the claim and leaves the token live; a spent token can't be
// peeked at.
func TestPeekJoinTokenDoesNotSpend(t *testing.T) {
	svc := joinService(t, nil)
	tok, err := svc.CreateNodeJoinToken(t.Context(), security.Principal{UserID: "admin", OrgID: "org"}, NodeJoinScope{Labels: map[string]string{"zone": "a"}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		claim, perr := svc.PeekNodeJoinToken(t.Context(), tok.Secret)
		if perr != nil || claim.OrgID != "org" || claim.Scope.Labels["zone"] != "a" {
			t.Fatalf("peek: %+v %v", claim, perr)
		}
	}
	if _, err = svc.ConsumeNodeJoinToken(t.Context(), tok.Secret); err != nil {
		t.Fatalf("the token was spent by a peek: %v", err)
	}
	if _, err = svc.PeekNodeJoinToken(t.Context(), tok.Secret); !errors.Is(err, ErrRejected) {
		t.Fatalf("peek of a spent token: %v", err)
	}
	if _, err = svc.PeekNodeJoinToken(t.Context(), "lp_live_nope"); !errors.Is(err, ErrRejected) {
		t.Fatalf("peek of an API key: %v", err)
	}
}

package apikeys

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xraph/keysmith"
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

type joinAudit struct {
	mu      sync.Mutex
	actions []string
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
	p := security.Principal{UserID: "admin", OrgID: "org", KeyID: "key-1"}
	scope := NodeJoinScope{CloudID: "cloud", Labels: map[string]string{"zone": "a"}, Endpoints: []nodeproto.Endpoint{nodeproto.EndpointControl, nodeproto.EndpointDocker}, Edge: true, EdgeScope: EdgeScopeOrg}
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
	if claim.OrgID != "org" || claim.MintedBy != "admin" || claim.MintedByKeyID != "key-1" {
		t.Fatalf("wrong claim: %+v", claim)
	}
	if claim.Scope.CloudID != "cloud" || claim.Scope.Labels["zone"] != "a" || len(claim.Scope.Endpoints) != 2 || claim.Scope.Endpoints[1] != nodeproto.EndpointDocker || !claim.Scope.Edge || claim.Scope.EdgeScope != EdgeScopeOrg {
		t.Fatalf("scope lost: %+v", claim.Scope)
	}
	if len(audit.actions) != 1 || audit.actions[0] != "node.join_token.created" {
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
	// Even dressed in the API key prefix, a kind other than fleet is refused.
	disguised := Prefix + tok.Secret[len(NodeJoinPrefix):]
	if _, err = svc.Authenticate(t.Context(), disguised); !errors.Is(err, ErrRejected) {
		t.Fatalf("disguised token authenticated: %v", err)
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
	for _, sc := range []NodeJoinScope{{EdgeScope: "galaxy", Edge: true}, {EdgeScope: EdgeScopeOrg}, {Labels: map[string]string{"": "x"}}} {
		if _, err := svc.CreateNodeJoinToken(t.Context(), p, sc, time.Minute); !errors.Is(err, ErrInvalid) {
			t.Fatalf("scope %+v: %v", sc, err)
		}
	}
}

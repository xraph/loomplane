package apikeys

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

func TestListNodeJoinTokensShowsOnlyUnspent(t *testing.T) {
	audit := &joinAudit{}
	svc := joinService(t, audit)
	admin := security.Principal{UserID: "admin", OrgID: "org"}
	mk, err := svc.Create(t.Context(), admin, NewKey{Name: "ci", Access: Write})
	if err != nil {
		t.Fatal(err)
	}
	withKey := admin
	withKey.KeyID = mk.Key.ID
	scope := NodeJoinScope{CloudID: "cloud", Labels: map[string]string{"zone": "a"}, Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker}, Edge: true}
	pending, err := svc.CreateNodeJoinToken(t.Context(), withKey, scope, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	spent, err := svc.CreateNodeJoinToken(t.Context(), admin, NodeJoinScope{}, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ConsumeNodeJoinToken(t.Context(), spent.Secret); err != nil {
		t.Fatal(err)
	}
	expiring, err := svc.CreateNodeJoinToken(t.Context(), admin, NodeJoinScope{}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.CreateNodeJoinToken(t.Context(), security.Principal{UserID: "admin", OrgID: "other"}, NodeJoinScope{}, 10*time.Minute); err != nil {
		t.Fatal(err)
	}

	got, err := svc.ListNodeJoinTokens(t.Context(), admin)
	if err != nil {
		t.Fatal(err)
	}
	ids := func(ts []PendingNodeJoinToken) []string {
		out := make([]string, 0, len(ts))
		for _, tk := range ts {
			out = append(out, tk.ID)
		}
		return out
	}
	if len(got) != 2 || !slices.Contains(ids(got), pending.ID) || !slices.Contains(ids(got), expiring.ID) {
		t.Fatalf("listed %v, want the two unspent tokens of this org", ids(got))
	}
	var p PendingNodeJoinToken
	for _, tk := range got {
		if tk.ID == pending.ID {
			p = tk
		}
	}
	if p.MintedBy != "admin" || p.MintedByKeyID != mk.Key.ID || p.CloudID != "cloud" || p.Labels["zone"] != "a" ||
		!slices.Equal(p.Endpoints, []nodeproto.Endpoint{nodeproto.EndpointDocker}) || !p.Edge || p.EdgeScope != EdgeScopeOrg ||
		p.ExpiresAt.IsZero() || p.CreatedAt.IsZero() || p.Hint == "" || p.Hint == pending.Secret {
		t.Fatalf("pending token %+v", p)
	}
	svc.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if got, err = svc.ListNodeJoinTokens(t.Context(), admin); err != nil || len(got) != 1 || got[0].ID != pending.ID {
		t.Fatalf("after one expired: %v %v", ids(got), err)
	}
	if !slices.Contains(audit.actions, "node.join_token.listed") {
		t.Fatalf("listing not audited: %v", audit.actions)
	}
}

// Who may list and cancel is who may mint: not a developer, a read key, the
// system or a fleet.
func TestPendingJoinTokensNeedMintRights(t *testing.T) {
	svc := joinService(t, nil)
	tok, err := svc.CreateNodeJoinToken(t.Context(), security.Principal{UserID: "admin", OrgID: "org"}, NodeJoinScope{}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]security.Principal{
		"developer": {UserID: "dev", OrgID: "org"},
		"read key":  {UserID: "admin", OrgID: "org", KeyID: "key", ReadOnly: true},
		"system":    security.SystemPrincipal("org"),
	} {
		if _, err := svc.ListNodeJoinTokens(t.Context(), p); !errors.Is(err, security.ErrForbidden) {
			t.Errorf("%s listed: %v", name, err)
		}
		if err := svc.CancelNodeJoinToken(t.Context(), p, tok.ID); !errors.Is(err, security.ErrForbidden) {
			t.Errorf("%s cancelled: %v", name, err)
		}
	}
	if _, err := svc.PeekNodeJoinToken(t.Context(), tok.Secret); err != nil {
		t.Fatalf("a refused cancel spent the token: %v", err)
	}
}

func TestCancelNodeJoinToken(t *testing.T) {
	audit := &joinAudit{}
	svc := joinService(t, audit)
	admin := security.Principal{UserID: "admin", OrgID: "org"}
	tok, err := svc.CreateNodeJoinToken(t.Context(), admin, NodeJoinScope{}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Another org can't see it, let alone cancel it.
	if err = svc.CancelNodeJoinToken(t.Context(), security.Principal{UserID: "admin", OrgID: "other"}, tok.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancelled from another org: %v", err)
	}
	if err = svc.CancelNodeJoinToken(t.Context(), admin, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ConsumeNodeJoinToken(t.Context(), tok.Secret); !errors.Is(err, ErrRejected) {
		t.Fatalf("a cancelled token joined: %v", err)
	}
	if err = svc.CancelNodeJoinToken(t.Context(), admin, tok.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancelled twice: %v", err)
	}
	if !slices.Contains(audit.actions, "node.join_token.cancelled") {
		t.Fatalf("cancel not audited: %v", audit.actions)
	}
	last := audit.metas[len(audit.metas)-1]
	if last["minted_by"] != "admin" {
		t.Fatalf("cancel audit meta %v", last)
	}
	// An API key is no join token, and neither is a bad id.
	mk, err := svc.Create(t.Context(), admin, NewKey{Name: "ci", Access: Write})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{mk.Key.ID, "nope"} {
		if err = svc.CancelNodeJoinToken(t.Context(), admin, bad); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cancelled %q: %v", bad, err)
		}
	}
	if _, err = svc.Authenticate(t.Context(), mk.Secret); err != nil {
		t.Fatalf("the API key stopped working: %v", err)
	}
}

// An instance edge token can only be cancelled by who could mint it: a
// platform admin, signed in.
func TestCancelInstanceEdgeNeedsPlatformAdmin(t *testing.T) {
	svc := joinService(t, nil)
	platform := security.Principal{UserID: "platform", OrgID: "org"}
	tok, err := svc.CreateNodeJoinToken(t.Context(), platform, NodeJoinScope{Edge: true, EdgeScope: EdgeScopeInstance}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.CancelNodeJoinToken(t.Context(), security.Principal{UserID: "admin", OrgID: "org"}, tok.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("org admin cancelled an instance edge: %v", err)
	}
	withKey := platform
	withKey.KeyID = "key"
	if err = svc.CancelNodeJoinToken(t.Context(), withKey, tok.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("cancelled an instance edge with a key: %v", err)
	}
	if err = svc.CancelNodeJoinToken(t.Context(), platform, tok.ID); err != nil {
		t.Fatal(err)
	}
}

// A cancel racing a join: exactly one of them wins the token.
func TestCancelRacesJoin(t *testing.T) {
	svc := joinService(t, nil)
	admin := security.Principal{UserID: "admin", OrgID: "org"}
	for round := range 25 {
		tok, err := svc.CreateNodeJoinToken(t.Context(), admin, NodeJoinScope{}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var joinErr, cancelErr error
		start := make(chan struct{})
		wg.Go(func() { <-start; _, joinErr = svc.ConsumeNodeJoinToken(t.Context(), tok.Secret) })
		wg.Go(func() { <-start; cancelErr = svc.CancelNodeJoinToken(t.Context(), admin, tok.ID) })
		close(start)
		wg.Wait()
		joined, cancelled := joinErr == nil, cancelErr == nil
		if joined == cancelled {
			t.Fatalf("round %d: join %v, cancel %v; want exactly one to win", round, joinErr, cancelErr)
		}
		if !joined && !errors.Is(joinErr, ErrRejected) || !cancelled && !errors.Is(cancelErr, ErrNotFound) {
			t.Fatalf("round %d: losers got join %v, cancel %v", round, joinErr, cancelErr)
		}
	}
}

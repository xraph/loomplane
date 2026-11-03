package nodestest

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/security"
)

// Org and User are who the test join token belongs to.
const (
	Org  = "org_1"
	User = "usr_1"
)

// Principal is a member who may manage nodes.
var Principal = security.Principal{UserID: User, OrgID: Org}

// Tokens is a join token spender that accepts every token.
type Tokens struct {
	mu    sync.Mutex
	spent int
}

// Spent is how many tokens have been spent.
func (t *Tokens) Spent() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.spent
}

// PeekNodeJoinToken implements nodes.JoinTokens.
func (t *Tokens) PeekNodeJoinToken(context.Context, string) (*apikeys.NodeJoinClaim, error) {
	return &apikeys.NodeJoinClaim{OrgID: Org, MintedBy: User, MintedByKeyID: "key_1"}, nil
}

// ConsumeNodeJoinToken implements nodes.JoinTokens.
func (t *Tokens) ConsumeNodeJoinToken(context.Context, string) (*apikeys.NodeJoinClaim, error) {
	t.mu.Lock()
	t.spent++
	t.mu.Unlock()
	return &apikeys.NodeJoinClaim{OrgID: Org, MintedBy: User, MintedByKeyID: "key_1"}, nil
}

type allowAll struct{}

func (allowAll) Allowed(context.Context, security.Principal, security.Permission) (bool, error) {
	return true, nil
}

// NewService returns a service over store and tokens that lets every member
// manage nodes. The caller sets its Notify, to whichever hub is current.
func NewService(store nodes.Store, tokens nodes.JoinTokens) *nodes.Service {
	return &nodes.Service{Store: store, Tokens: tokens, Authz: allowAll{}, Version: "v1.8.0"}
}

// NewHub returns a hub for audience, bound to store and svc, that logs nothing.
func NewHub(t testing.TB, audience string, store nodes.Store, svc *nodes.Service) *nodes.Hub {
	t.Helper()
	h := nodes.NewHub(nodes.HubConfig{
		Audience: audience, Replica: "test",
		HopSecret: []byte("hop-secret-for-tests-only-000000"), ChallengeSecret: []byte("challenge-secret-for-tests-00000"),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := h.Bind(store, svc); err != nil {
		t.Fatal(err)
	}
	return h
}

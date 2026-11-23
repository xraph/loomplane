package nodes

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

type fakeRunnerPools struct {
	err    error
	joined []string
}

func (f *fakeRunnerPools) JoinRunner(_ context.Context, org, pool, node string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.joined = append(f.joined, org+"/"+pool+"/"+node)
	return "conn_pool", nil
}

func TestRunnerJoinsItsPool(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{Role: apikeys.RoleRunner, PoolID: "rpool_1", Endpoints: []nodeproto.Endpoint{nodeproto.EndpointFirecracker, nodeproto.EndpointRelay}}, true)
	pools := &fakeRunnerPools{}
	adopted := false
	r.svc.Runners = pools
	r.svc.Adopt = func(context.Context, *Node, *apikeys.NodeJoinClaim) error { adopted = true; return nil }
	res, err := r.svc.Join(context.Background(), signed(runnerReq(nodeproto.EndpointFirecracker, nodeproto.EndpointRelay)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Role != RoleRunner || res.PoolID != "rpool_1" || res.PoolConnection != "conn_pool" {
		t.Fatalf("result %+v", res)
	}
	n, _ := r.store.GetByID(context.Background(), res.NodeID)
	if n.Role != RoleRunner {
		t.Fatalf("role %q", n.Role)
	}
	if len(pools.joined) != 1 || pools.joined[0] != "org_1/rpool_1/"+res.NodeID {
		t.Fatalf("joined %v", pools.joined)
	}
	if adopted {
		t.Fatal("a runner was adopted into a Cloud as a server")
	}
}

func TestRunnerJoinRolledBackWhenThePoolRefuses(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{Role: apikeys.RoleRunner, PoolID: "rpool_gone"}, true)
	r.svc.Runners = &fakeRunnerPools{err: errors.New("the runner pool isn't in this organization")}
	_, err := r.svc.Join(context.Background(), signed(runnerReq()))
	if !errors.Is(err, ErrMintNewToken) {
		t.Fatalf("err = %v", err)
	}
	nodes, _ := r.store.List(context.Background(), "org_1")
	for _, n := range nodes {
		if n.Status != StatusRevoked {
			t.Fatalf("a runner the pool refused stayed joined: %+v", n)
		}
	}
}

func TestRunnerTokenWithoutRunnerPools(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{Role: apikeys.RoleRunner, PoolID: "rpool_1"}, true)
	if _, err := r.svc.Join(context.Background(), signed(runnerReq())); !errors.Is(err, ErrMintNewToken) {
		t.Fatalf("err = %v", err)
	}
}

// runnerReq is a join from runner start.
func runnerReq(eps ...nodeproto.Endpoint) JoinRequest {
	in := joinReq(eps...)
	in.Role = RoleRunner
	return in
}

func TestTokensRefuseTheOtherRoleBeforeTheyAreSpent(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{Role: apikeys.RoleRunner, PoolID: "rpool_1"}, true)
	r.svc.Runners = &fakeRunnerPools{}
	if _, err := r.svc.Join(context.Background(), signed(joinReq())); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a runner's token joined a node: %v", err)
	}
	r2 := newRig(t, apikeys.NodeJoinScope{}, true)
	if _, err := r2.svc.Join(context.Background(), signed(runnerReq())); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a node's token joined a runner: %v", err)
	}
	if r.tokens.calls != 0 || r2.tokens.calls != 0 {
		t.Fatal("a token was spent on a join for the wrong role")
	}
}

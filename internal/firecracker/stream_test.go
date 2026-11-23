package firecracker

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
)

// streamPair serves h as a runner does, with no TLS and a node peer, and
// returns a stream client that reaches it.
func streamPair(t *testing.T, h Handler) *Client {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	addr := strings.TrimPrefix(server.URL, "http://")
	client, err := NewStreamClient("host", func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func runnerBuild(q Request) Request {
	q.Spec = Spec{Kind: "build", SourceDigest: strings.Repeat("c", 64), Dockerfile: ".loomplane-build/Dockerfile", BuildImage: "registry.example/acme/loomplane/api:" + strings.Repeat("d", 24), VCPUs: 2, MemoryMiB: 2048, DiskMiB: 16384}
	q.Values = Values{}
	q.Authority.Operation = "build-1"
	return q
}

func TestStreamClientReachesARunnerHost(t *testing.T) {
	h, d, q := testHost(t)
	client := streamPair(t, Handler{Host: h, Node: &Peer{Org: "org", Connection: "conn"}, BuildsOnly: true})
	caps, err := client.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caps.HostID != "host" || client.Details["build_isolation"] != "ready" || client.Details["runtime_identity"] != strings.Repeat("a", 64) || client.Details["architecture"] != "amd64" {
		t.Fatalf("capabilities %+v, details %v", caps, client.Details)
	}
	if _, err := client.Apply(context.Background(), runnerBuild(q)); err != nil {
		t.Fatalf("a build VM must be accepted: %v", err)
	}
	if d.starts != 1 {
		t.Fatalf("starts = %d, want 1", d.starts)
	}
}

func TestRunnerHostRefusesWorkloadsAndOtherScopes(t *testing.T) {
	h, d, q := testHost(t)
	client := streamPair(t, Handler{Host: h, Node: &Peer{Org: "org", Connection: "conn"}, BuildsOnly: true})
	if _, err := client.Apply(context.Background(), q); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("an application VM on a runner: %v", err)
	}
	other := runnerBuild(q)
	other.Scope.Org = "another-org"
	if _, err := client.Apply(context.Background(), other); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("another organization's build on this runner: %v", err)
	}
	other = runnerBuild(q)
	other.Scope.Connection = "another-pool"
	if _, err := client.Apply(context.Background(), other); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("another pool's build on this runner: %v", err)
	}
	if d.starts != 0 {
		t.Fatalf("starts = %d, want none", d.starts)
	}
}

func TestHostWithoutNodePeerRefusesPlainRequests(t *testing.T) {
	h, _, q := testHost(t)
	client := streamPair(t, Handler{Host: h})
	if _, err := client.Check(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("capabilities without a client identity: %v", err)
	}
	if _, err := client.Apply(context.Background(), runnerBuild(q)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a build without a client identity: %v", err)
	}
}

func TestStreamClientNeedsAHostIDAndDialer(t *testing.T) {
	if _, err := NewStreamClient("", func(context.Context) (net.Conn, error) { return nil, nil }); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty host ID: %v", err)
	}
	if _, err := NewStreamClient("host", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil dialer: %v", err)
	}
}

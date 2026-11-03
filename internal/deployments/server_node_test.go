package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

const testNodeID = "node_11111111-2222-4333-8444-555555555555"

// fakeNodeStreams stands in for the nodes hub. Each docker stream it opens
// reaches daemon, the address of a fake Docker daemon, unless err is set,
// which Open returns instead, as the hub does for an offline node or a
// refused endpoint.
type fakeNodeStreams struct {
	t      *testing.T
	daemon string
	err    error

	mu     sync.Mutex
	opened []nodeproto.StreamHeader
}

func (f *fakeNodeStreams) Open(ctx context.Context, orgID, nodeID string, hdr nodeproto.StreamHeader) (net.Conn, error) {
	f.mu.Lock()
	f.opened = append(f.opened, hdr)
	f.mu.Unlock()
	if orgID != "org_fixture" || nodeID != testNodeID {
		f.t.Errorf("a stream went to node %s of %s", nodeID, orgID)
		return nil, errors.New("another node")
	}
	if f.err != nil {
		return nil, f.err
	}
	if hdr.Endpoint != nodeproto.EndpointDocker || hdr.Target != nodeproto.DockerTargetSocket {
		f.t.Errorf("unexpected node stream %+v", hdr)
		return nil, errors.New("unexpected stream")
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", f.daemon)
}

func (f *fakeNodeStreams) streams() []nodeproto.StreamHeader {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]nodeproto.StreamHeader(nil), f.opened...)
}

// nodeConnection is a ready server connection that reaches testNodeID's
// Docker, as adoption makes one, with its synthesized credential.
func nodeConnection(connectionID, name string, extra map[string]string) (*connections.Connection, []byte) {
	details := map[string]string{"node_id": testNodeID, "identity_generation": "1", "docker_socket": "/var/run/docker.sock", "docker_id": "node-daemon", "architecture": "amd64"}
	maps.Copy(details, extra)
	return &connections.Connection{ID: connectionID, OrgID: "org_fixture", Cloud: connections.Server, Method: connections.MethodNode, Name: name, Status: connections.StatusReady, Details: details},
		server.NodeCredential("org_fixture", testNodeID)
}

func credentialsFor(conn *connections.Connection, raw []byte) buildCredentialFunc {
	return func(_ context.Context, org, id string) (*connections.Connection, []byte, error) {
		if org != conn.OrgID || id != conn.ID {
			return nil, nil, connections.ErrNotFound
		}
		return conn, raw, nil
	}
}

// nodeLabels are an instance's labels on conn, with its reviewed settings.
func nodeLabels(t *testing.T, conn *connections.Connection) map[string]string {
	t.Helper()
	reviewed, err := json.Marshal(planning.ReviewedConnectionSettings(conn.Cloud, conn.Details))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{owned.LabelOrg: conn.OrgID, owned.LabelEnvironment: "env_fixture", owned.LabelProject: "proj_fixture", owned.LabelConnection: conn.ID,
		owned.LabelSettings: string(reviewed), owned.LabelRuntime: "vault_reference", owned.LabelPublic: "false"}
}

// nodeSessionError is what a server session on a node connection returns
// when the hub's Open fails with err.
func nodeSessionError(t *testing.T, err error) error {
	t.Helper()
	conn, raw := nodeConnection("conn_node", "web-1", nil)
	target := &OwnedTarget{Cloud: connections.Server, Credentials: credentialsFor(conn, raw), NodeStreams: &fakeNodeStreams{t: t, err: err}}
	client, sessionErr := target.serverSession(conn.OrgID, conn.ID)(t.Context(), nodeLabels(t, conn))
	if sessionErr == nil {
		_ = client.Close()
		t.Fatal("a session reached a node whose streams fail")
	}
	return sessionErr
}

func TestServerSessionNodeMarker(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	var mu sync.Mutex
	created, started := false, false
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/version":
			_, _ = io.WriteString(w, `{"Version":"29.8.2","APIVersion":"1.54","Os":"linux","Arch":"amd64"}`)
		case r.URL.Path == "/info":
			_, _ = io.WriteString(w, `{"ID":"node-daemon","OSType":"linux","Architecture":"x86_64"}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json"):
			if !created {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "container_one", "Image": "sha256:" + strings.Repeat("a", 64), "Config": map[string]any{"Labels": map[string]string{owned.LabelInstance: iid.String(), owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture"}}, "State": map[string]any{"Running": started, "Status": "running"}})
		case r.URL.Path == "/networks/"+server.NetworkName("org_fixture", "env_fixture"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "network_one", "Labels": map[string]string{owned.LabelOrg: "org_fixture", owned.LabelEnvironment: "env_fixture"}})
		case r.Method == http.MethodPost && r.URL.Path == "/containers/create":
			created = true
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"Id":"container_one"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start"):
			started = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(daemon.Close)
	conn, raw := nodeConnection("conn_node", "web-1", nil)
	streams := &fakeNodeStreams{t: t, daemon: daemon.Listener.Addr().String()}
	target := &OwnedTarget{Cloud: connections.Server, Credentials: credentialsFor(conn, raw), NodeStreams: streams}
	runtime := &server.Runtime{Session: target.serverSession(conn.OrgID, conn.ID), Values: func(context.Context, map[string]string) (owned.RuntimeValues, error) {
		return owned.RuntimeValues{Env: map[string]string{}}, nil
	}}
	result, err := runtime.Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: "sha256:" + strings.Repeat("a", 64), Ports: []cpprovider.PortSpec{{Container: 8080}}}, nodeLabels(t, conn))
	if err != nil {
		t.Fatal(err)
	}
	if !created || !started || len(result.Endpoints) != 1 {
		t.Fatalf("the node's Docker didn't run the container: created %v, started %v, %+v", created, started, result)
	}
	if len(streams.streams()) == 0 {
		t.Fatal("the session didn't reach Docker through the node's streams")
	}
}

func TestBuildThroughNode(t *testing.T) {
	rig := newImageBuildRig(t, false)
	save := func() error { return nil }
	// The same source built on an SSH builder gives the context's digest.
	if _, err := rig.builder.Build(t.Context(), rig.job, &ImageArtifact{}, save); err != nil {
		t.Fatal(err)
	}
	conn, raw := nodeConnection("conn_builder", "build-1", map[string]string{"build_workers": "dedicated", "docker_id": "build-fixture"})
	rig.job.Action.Service.BuildTarget.BuilderSettings = planning.ReviewedBuildSettings(conn.Cloud, conn.Details)
	rig.job.BuildCredentials = credentialsFor(conn, raw)
	streams := &fakeNodeStreams{t: t, daemon: rig.daemon}
	image, err := (&ImageBuilder{NodeStreams: streams}).Build(t.Context(), rig.job, &ImageArtifact{}, save)
	if err != nil || image == "" {
		t.Fatalf("build on the node: %q %v", image, err)
	}
	tars := *rig.tars
	if *rig.builds != 2 || len(tars) != 2 || tars[0] != tars[1] {
		t.Fatalf("the build context didn't arrive intact through the node: %d builds, digests %v", *rig.builds, tars)
	}
	if len(streams.streams()) == 0 {
		t.Fatal("the build didn't go through the node's streams")
	}
}

func TestDedicatedNodeBuilderAllowed(t *testing.T) {
	for _, tc := range []struct {
		workers string
		allowed bool
	}{{"dedicated", true}, {"", false}, {"shared", false}} {
		t.Run("workers="+tc.workers, func(t *testing.T) {
			rig := newImageBuildRig(t, false)
			conn, raw := nodeConnection("conn_builder", "build-1", map[string]string{"build_workers": tc.workers, "docker_id": "build-fixture"})
			rig.job.Action.Service.BuildTarget.BuilderSettings = planning.ReviewedBuildSettings(conn.Cloud, conn.Details)
			rig.job.BuildCredentials = credentialsFor(conn, raw)
			_, err := (&ImageBuilder{NodeStreams: &fakeNodeStreams{t: t, daemon: rig.daemon}}).Build(t.Context(), rig.job, &ImageArtifact{}, func() error { return nil })
			if tc.allowed && err != nil {
				t.Fatalf("a dedicated node builder refused an untrusted source: %v", err)
			}
			if !tc.allowed && (err == nil || !strings.Contains(err.Error(), "untrusted builds") || *rig.builds != 0) {
				t.Fatalf("a %q node builder took an untrusted source: %d builds, %v", tc.workers, *rig.builds, err)
			}
		})
	}
}

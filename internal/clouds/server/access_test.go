package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// fakeAccess is a HostAccess whose Docker connections reach a fake daemon
// listening on TCP. A method the test leaves unset fails the test when
// called, so a path that only SSH could serve shows up as a failure.
type fakeAccess struct {
	t         *testing.T
	daemon    string
	listeners func() ([]int, error)

	mu     sync.Mutex
	modes  []string
	fenced []string
	closed bool
}

func (f *fakeAccess) DialContext(_ context.Context, network, address string) (net.Conn, error) {
	f.t.Errorf("unexpected host dial %s %s", network, address)
	return nil, errors.New("unexpected host dial")
}

func (f *fakeAccess) DialDocker(ctx context.Context) (net.Conn, error) {
	if f.daemon == "" {
		f.t.Error("unexpected Docker dial")
		return nil, errors.New("unexpected Docker dial")
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", f.daemon)
}

// FencedRequest stands in for the host's fence: advance succeeds, every
// other mode forwards the one request to the fake daemon.
func (f *fakeAccess) FencedRequest(ctx context.Context, mode string, fence EffectFence, daemon, socket, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	f.mu.Lock()
	f.modes = append(f.modes, mode)
	f.fenced = append(f.fenced, daemon+" "+socket+" "+fence.Operation)
	f.mu.Unlock()
	if mode == "advance" {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header = headers.Clone()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return f.DialDocker(ctx) }, DisableKeepAlives: true}
	return transport.RoundTrip(req)
}

func (f *fakeAccess) CgroupLimits(context.Context) (int64, int64, error) {
	f.t.Error("unexpected cgroup probe")
	return 0, 0, errors.New("unexpected cgroup probe")
}

func (f *fakeAccess) GPUProbe(context.Context) ([]byte, error) {
	f.t.Error("unexpected GPU probe")
	return nil, errors.New("unexpected GPU probe")
}

func (f *fakeAccess) Filesystem(context.Context, string) (FilesystemCapacity, error) {
	f.t.Error("unexpected filesystem probe")
	return FilesystemCapacity{}, errors.New("unexpected filesystem probe")
}

func (f *fakeAccess) IngressListeners(context.Context) ([]int, error) {
	if f.listeners == nil {
		f.t.Error("unexpected listener probe")
		return nil, errors.New("unexpected listener probe")
	}
	return f.listeners()
}

func (f *fakeAccess) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

// fakeDaemon serves handler as a Docker daemon on loopback TCP.
func fakeDaemon(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)
	return api.Listener.Addr().String()
}

func identityDaemon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/version":
		_, _ = io.WriteString(w, `{"Version":"29.8.2","APIVersion":"1.53","Os":"linux","Arch":"arm64"}`)
	case "/info":
		_, _ = io.WriteString(w, `{"ID":"fake-daemon","OSType":"linux","Architecture":"aarch64"}`)
	case "/containers/json":
		_, _ = io.WriteString(w, `[]`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestConnectSSHUsesSSHAccess(t *testing.T) {
	cfg, _ := sshFixture(t)
	c, err := Connect(t.Context(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, ok := c.access.(*sshAccess); !ok {
		t.Fatalf("an SSH config reached the host through %T", c.access)
	}
	if details, missing, checkErr := c.Check(t.Context()); checkErr != nil || len(missing) != 0 || details["docker_id"] != "isolated-daemon" {
		t.Fatalf("check: %v %v %v", details, missing, checkErr)
	}

	// Injected: whatever access the SSH dialer returns carries the client's
	// Docker traffic, and closing the client closes it.
	fake := &fakeAccess{t: t, daemon: fakeDaemon(t, identityDaemon)}
	var dialed *Config
	injected, err := connect(t.Context(), cfg, &fakeStreams{t: t}, func(_ context.Context, got *Config) (HostAccess, error) {
		dialed = got
		return fake, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if dialed != cfg || injected.access != HostAccess(fake) || injected.Config != cfg {
		t.Fatalf("connect dialed %p for %p, access %T", dialed, cfg, injected.access)
	}
	if details, missing, err := injected.Check(t.Context()); err != nil || len(missing) != 0 || details["docker_id"] != "fake-daemon" {
		t.Fatalf("check through the injected access: %v %v %v", details, missing, err)
	}
	if err := injected.Close(); err != nil || !fake.closed {
		t.Fatalf("close: %v, closed %v", err, fake.closed)
	}
}

// fakeStreams fails the test when anything opens a node stream.
type fakeStreams struct{ t *testing.T }

func (s *fakeStreams) Open(_ context.Context, orgID, nodeID string, hdr nodeproto.StreamHeader) (net.Conn, error) {
	s.t.Errorf("unexpected node stream %s/%s %+v", orgID, nodeID, hdr)
	return nil, errors.New("unexpected node stream")
}

func TestConnectNodeWithoutStreamsIsInvalid(t *testing.T) {
	cfg := &Config{Socket: "/var/run/docker.sock", Node: &NodeTarget{OrgID: "org", NodeID: testNodeID, IdentityGeneration: 1}, Details: map[string]string{}}
	if c, err := Connect(t.Context(), cfg, nil); !errors.Is(err, ErrInvalid) || c != nil {
		t.Fatalf("node without streams: %v", err)
	}
	if c, err := connect(t.Context(), cfg, nil, func(context.Context, *Config) (HostAccess, error) {
		t.Fatal("a node config dialed SSH")
		return nil, nil
	}); !errors.Is(err, ErrInvalid) || c != nil {
		t.Fatalf("node without streams: %v", err)
	}
	if c, err := Connect(t.Context(), nil, nil); !errors.Is(err, ErrInvalid) || c != nil {
		t.Fatalf("nil config: %v", err)
	}
}

// testNodeID is a node ID in the form the nodes service makes them.
const testNodeID = "node_0b6f2c1e-4a8d-4c3b-9f1e-2d7a5c8e9b10"

func TestParseConfigNodeMarker(t *testing.T) {
	marker := NodeCredential("org_1", testNodeID)
	cfg, err := ParseConfig(marker, map[string]string{"node_id": testNodeID, "identity_generation": "3", "docker_socket": "/run/user/1000/docker.sock", "advertise_host": "box.example.com", "build_workers": "shared"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Node == nil || *cfg.Node != (NodeTarget{OrgID: "org_1", NodeID: testNodeID, IdentityGeneration: 3}) {
		t.Fatalf("node %+v", cfg.Node)
	}
	if cfg.Signer != nil || cfg.HostKey != nil || cfg.Address != "" || cfg.User != "" || cfg.Socket != "/run/user/1000/docker.sock" {
		t.Fatalf("config %+v", cfg)
	}
	want := map[string]string{"node_id": testNodeID, "identity_generation": "3", "docker_socket": "/run/user/1000/docker.sock", "advertise_host": "box.example.com", "build_workers": "shared"}
	if !maps.Equal(cfg.Details, want) {
		t.Fatalf("details %v", cfg.Details)
	}

	cfg, err = ParseConfig(marker, map[string]string{"node_id": testNodeID, "identity_generation": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Socket != "/var/run/docker.sock" || cfg.Details["docker_socket"] != "/var/run/docker.sock" || cfg.Details["advertise_host"] != "" {
		t.Fatalf("defaults %+v", cfg)
	}

	// node is the settings a node connection records, with extra on top.
	node := func(id string, extra ...string) map[string]string {
		settings := map[string]string{"node_id": id, "identity_generation": "1"}
		for i := 0; i+1 < len(extra); i += 2 {
			settings[extra[i]] = extra[i+1]
		}
		return settings
	}
	other := "node_7c1d9e2a-5b3f-4e6a-8d0c-1f2e3a4b5c6d"
	for name, tc := range map[string]struct {
		raw      []byte
		settings map[string]string
	}{
		"runtime class":          {marker, node(testNodeID, "build_runtime_class", "kata")},
		"bad build workers":      {marker, node(testNodeID, "build_workers", "everyone")},
		"no identity generation": {marker, map[string]string{"node_id": testNodeID}},
		"zero generation":        {marker, node(testNodeID, "identity_generation", "0")},
		"word generation":        {marker, node(testNodeID, "identity_generation", "three")},
		"relative socket":        {marker, node(testNodeID, "docker_socket", "docker.sock")},
		"bad advertise host":     {marker, node(testNodeID, "advertise_host", "a/b")},
		"other node in settings": {marker, node(other)},
		"no node in settings":    {marker, map[string]string{"identity_generation": "1"}},
		"empty node in settings": {marker, node("")},
		"empty node":             {NodeCredential("org_1", ""), node("")},
		"node without uuid":      {NodeCredential("org_1", "node_abc"), node("node_abc")},
		"node without prefix":    {NodeCredential("org_1", testNodeID[len("node_"):]), node(testNodeID[len("node_"):])},
		"node uppercase uuid":    {NodeCredential("org_1", strings.ToUpper(testNodeID)), node(strings.ToUpper(testNodeID))},
		"node braced uuid":       {NodeCredential("org_1", "node_{"+testNodeID[len("node_"):]+"}"), node("node_{" + testNodeID[len("node_"):] + "}")},
		"node urn uuid":          {NodeCredential("org_1", "node_urn:uuid:"+testNodeID[len("node_"):]), node("node_urn:uuid:" + testNodeID[len("node_"):])},
		"empty org":              {NodeCredential("", testNodeID), node(testNodeID)},
		"org with space":         {NodeCredential("org 1", testNodeID), node(testNodeID)},
		"org with tab":           {NodeCredential("org\t1", testNodeID), node(testNodeID)},
		"org with newline":       {NodeCredential("org_1\n", testNodeID), node(testNodeID)},
		"org with control":       {NodeCredential("org\x00", testNodeID), node(testNodeID)},
		"org with delete":        {NodeCredential("org\x7f", testNodeID), node(testNodeID)},
		"org not ascii":          {NodeCredential("orgé", testNodeID), node(testNodeID)},
		"org too long":           {NodeCredential(strings.Repeat("o", 129), testNodeID), node(testNodeID)},
		"marker with extras":     {[]byte(`{"loomplane_node":{"org_id":"org_1","node_id":"` + testNodeID + `"},"host":"evil.example"}`), node(testNodeID)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseConfig(tc.raw, tc.settings); !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted: %v", err)
			}
		})
	}
}

func TestNodeCredentialNotPastable(t *testing.T) {
	marker := NodeCredential("org_1", "node_abc")
	if string(marker) != `{"loomplane_node":{"org_id":"org_1","node_id":"node_abc"}}` {
		t.Fatalf("marker %s", marker)
	}
	if !IsNodeCredential(marker) {
		t.Fatal("the marker isn't a node credential")
	}
	_, pem := testKey(t)
	for name, raw := range map[string][]byte{
		"PEM key":    pem,
		"key JSON":   []byte(`{"private_key":"x","passphrase":"y"}`),
		"in-cluster": []byte(`{"type":"loomplane_in_cluster"}`),
		"null node":  []byte(`{"loomplane_node":null}`),
		"empty":      nil,
	} {
		if IsNodeCredential(raw) {
			t.Fatalf("%s is a node credential", name)
		}
	}
}

func TestCheckVacantUsesListeners(t *testing.T) {
	for _, tc := range []struct {
		name      string
		listeners func() ([]int, error)
		want      string
	}{
		{"https held", func() ([]int, error) { return []int{443}, nil }, "ingress listeners are already in use"},
		{"free", func() ([]int, error) { return nil, nil }, ""},
		{"unknown", func() ([]int, error) { return nil, errors.New("cannot verify ingress listener availability") }, "cannot verify ingress listener availability"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeAccess{t: t, daemon: fakeDaemon(t, identityDaemon), listeners: tc.listeners}
			err := newClient(&Config{Details: map[string]string{}}, fake).CheckVacant(t.Context())
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestParseListenersReadsSS(t *testing.T) {
	ports, err := parseListeners([]byte("LISTEN 0      4096         0.0.0.0:443        0.0.0.0:*\nLISTEN 0 4096 [::]:80 [::]:*\nLISTEN 0 4096 127.0.0.53%lo:2019 0.0.0.0:*\n\nLISTEN 0 4096 [::]:443 [::]:*\n"))
	if err != nil || !slices.Equal(ports, []int{80, 443, 2019}) {
		t.Fatalf("ports %v: %v", ports, err)
	}
	if ports, err := parseListeners([]byte("\n")); err != nil || len(ports) != 0 {
		t.Fatalf("empty: %v %v", ports, err)
	}
	for _, raw := range []string{"LISTEN 0 4096\n", "LISTEN 0 4096 0.0.0.0:http 0.0.0.0:*\n", "LISTEN 0 4096 0.0.0.0:0 0.0.0.0:*\n"} {
		if _, err := parseListeners([]byte(raw)); err == nil {
			t.Fatalf("parsed %q", raw)
		}
	}
}

func TestEnsureIngressThroughFakeAccess(t *testing.T) {
	const (
		org, server = "org", "server"
		containerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		execID      = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	)
	name := IngressName(org, server)
	var (
		mu               sync.Mutex
		created, started bool
		image            string
		volumes          []string
		archive          bool
	)
	daemon := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		route := r.Method + " " + r.URL.Path
		switch {
		case route == "GET /containers/"+name+"/json":
			if !created {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": containerID, "State": map[string]any{"Running": started}, "Config": map[string]any{"Image": image, "Labels": ingressLabels(org, server)}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/images/"):
			w.WriteHeader(http.StatusNotFound)
		case route == "POST /images/create":
			_, _ = io.WriteString(w, `{"status":"pulled"}`)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/volumes/"):
			w.WriteHeader(http.StatusNotFound)
		case route == "POST /volumes/create":
			var body struct{ Name string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			volumes = append(volumes, body.Name)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{}`)
		case route == "POST /containers/create" && r.URL.Query().Get("name") == name:
			var body struct{ Image string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			created, image = true, body.Image
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"Id":"`+containerID+`"}`)
		case route == "PUT /containers/"+containerID+"/archive":
			archive = true
		case route == "POST /containers/"+containerID+"/start":
			started = true
			w.WriteHeader(http.StatusNoContent)
		case route == "POST /containers/"+containerID+"/exec":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"Id":"`+execID+`"}`)
		case route == "POST /exec/"+execID+"/start":
			w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
			out := []byte("etag-1\n{\"apps\":{}}")
			frame := make([]byte, 8, 8+len(out))
			frame[0] = 1
			binary.BigEndian.PutUint32(frame[4:], uint32(len(out)))
			_, _ = w.Write(append(frame, out...))
		case route == "GET /exec/"+execID+"/json":
			_, _ = io.WriteString(w, `{"Running":false,"ExitCode":0}`)
		default:
			t.Errorf("unexpected Docker request %s", route)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	fake := &fakeAccess{t: t, daemon: daemon}
	c := newClient(&Config{Socket: "/var/run/docker.sock", Details: map[string]string{"docker_id": "fake-daemon"}}, fake)
	c.RequireFence = true
	ctx := WithEffectFence(t.Context(), EffectFence{Operation: org + "/ingress/" + server, Generation: 1})
	snapshot, err := EnsureIngress(ctx, c, org, server)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ContainerID != containerID || snapshot.ETag != "etag-1" || string(snapshot.Config) != `{"apps":{}}` {
		t.Fatalf("snapshot %+v", snapshot)
	}
	mu.Lock()
	defer mu.Unlock()
	if !created || !started || !archive || image != ingressCreateImage() || !slices.Equal(volumes, []string{name + "-data", name + "-config"}) {
		t.Fatalf("created %v started %v archive %v image %q volumes %v", created, started, archive, image, volumes)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.modes[0] != "advance" || !slices.Contains(fake.modes, "request") || fake.modes[len(fake.modes)-1] != "ingress-exec" {
		t.Fatalf("fence modes %v", fake.modes)
	}
	for _, seen := range fake.fenced {
		if seen != "fake-daemon /var/run/docker.sock org/ingress/server" {
			t.Fatalf("fenced as %q", seen)
		}
	}
}

package deployments

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	"golang.org/x/crypto/ssh"
	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

// sshDocker is a server reached over SSH: a Docker daemon on a Unix
// socket, and TCP forwards to what the server's network reaches.
type sshDocker struct {
	details   map[string]string
	key       string
	mu        sync.Mutex
	ports     map[string]string // container name to its 9090/tcp host port
	inspected []string
	closed    chan struct{}
}

func newSSHDocker(t *testing.T) *sshDocker {
	t.Helper()
	host, _ := sshKey(t)
	user, raw := sshKey(t)
	d := &sshDocker{key: raw, ports: map[string]string{}, closed: make(chan struct{}, 4)}
	dir, err := os.MkdirTemp("/tmp", "lp-ssh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "docker.sock")
	unix, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	daemon := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/version":
			_, _ = io.WriteString(w, `{"Version":"29.8.2","APIVersion":"1.53","Os":"linux","Arch":"amd64"}`)
		case r.URL.Path == "/info":
			_, _ = io.WriteString(w, `{"ID":"isolated-daemon","OSType":"linux","Architecture":"x86_64"}`)
		case strings.HasPrefix(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/json")
			d.mu.Lock()
			d.inspected = append(d.inspected, name)
			port, ok := d.ports[name]
			d.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, `{"NetworkSettings":{"Ports":{"9090/tcp":[{"HostIp":"127.0.0.1","HostPort":"`+port+`"}]}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})}
	go func() { _ = daemon.Serve(unix) }()
	t.Cleanup(func() { _ = daemon.Close() })

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	sc := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if string(key.Marshal()) != string(user.PublicKey().Marshal()) {
			return nil, errors.New("refused")
		}
		return nil, nil
	}}
	sc.AddHostKey(host)
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go d.serve(t, conn, sc, socket)
		}
	}()
	hostname, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	d.details = map[string]string{
		"host": hostname, "advertise_host": hostname, "port": port, "user": "deploy", "docker_socket": socket,
		"host_key":     strings.TrimSpace(string(ssh.MarshalAuthorizedKey(host.PublicKey()))),
		"architecture": "amd64", "docker_id": "isolated-daemon",
	}
	return d
}

func (d *sshDocker) serve(t *testing.T, conn net.Conn, sc *ssh.ServerConfig, socket string) {
	ss, channels, requests, err := ssh.NewServerConn(conn, sc)
	if err != nil {
		_ = conn.Close()
		return
	}
	go ssh.DiscardRequests(requests)
	go func() {
		_ = ss.Wait()
		d.closed <- struct{}{}
	}()
	for channel := range channels {
		network, address := "unix", socket
		if channel.ChannelType() == "direct-tcpip" {
			var forward struct {
				Host       string
				Port       uint32
				OriginHost string
				OriginPort uint32
			}
			if ssh.Unmarshal(channel.ExtraData(), &forward) != nil {
				_ = channel.Reject(ssh.Prohibited, "forward refused")
				continue
			}
			network, address = "tcp", net.JoinHostPort(forward.Host, strconv.Itoa(int(forward.Port)))
		} else if channel.ChannelType() != "direct-streamlocal@openssh.com" {
			_ = channel.Reject(ssh.Prohibited, "refused")
			continue
		}
		remote, dialErr := (&net.Dialer{}).DialContext(t.Context(), network, address)
		if dialErr != nil {
			_ = channel.Reject(ssh.ConnectionFailed, "unavailable")
			continue
		}
		local, channelRequests, acceptErr := channel.Accept()
		if acceptErr != nil {
			_ = remote.Close()
			continue
		}
		go ssh.DiscardRequests(channelRequests)
		go func() { _, _ = io.Copy(local, remote); _ = local.Close(); _ = remote.Close() }()
		go func() { _, _ = io.Copy(remote, local); _ = remote.Close(); _ = local.Close() }()
	}
}

func TestServerComponentTransportReachesTheLoopbackPort(t *testing.T) {
	daemon := newSSHDocker(t)
	component := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "admin "+r.URL.Path)
	}))
	defer component.Close()
	_, published, err := net.SplitHostPort(component.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	details := map[string]string{"key": daemon.key}
	for k, v := range daemon.details {
		details[k] = v
	}
	f := newComponentFixture(t, recipestest.Registry(t))
	f.target.Credentials = dialCredentials{"conn": {ID: "conn", OrgID: "org", Cloud: connections.Server, Status: connections.StatusReady, Details: details}}
	f.secrets.m[resourceKey("org", "env", "app--postgres", "url")] = []byte(postgresURL)
	job := f.componentJob(t, "api", newPostgres)
	job.Action.Resource.TargetSettings = planning.ReviewedConnectionSettings(connections.Server, details)
	out, err := f.target.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	iid, err := id.Parse(out.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	container := server.ContainerName(iid)
	daemon.mu.Lock()
	daemon.ports[container] = published
	daemon.mu.Unlock()

	transport, closer, err := f.target.ComponentTransport(t.Context(), bound(job, out), 9090)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://component/admin/api", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if string(got) != "admin /admin/api" {
		t.Fatalf("component answered %q", got)
	}
	daemon.mu.Lock()
	inspected := append([]string(nil), daemon.inspected...)
	daemon.mu.Unlock()
	if len(inspected) != 1 || inspected[0] != container {
		t.Fatalf("inspected %v, want %s", inspected, container)
	}
	// Closing ends the SSH connection, and nothing more goes through it.
	if err = closer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-daemon.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the channel left its SSH connection open")
	}
	again, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://component/admin/api", http.NoBody)
	if res, err := client.Do(again); err == nil {
		_ = res.Body.Close()
		t.Fatal("reached the component after closing")
	}
}

func TestKubernetesComponentTransportNeedsServiceProxy(t *testing.T) {
	var proxyAllowed atomic.Bool
	var proxied atomic.Value
	apiserver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/version":
			_, _ = w.Write([]byte(`{"gitVersion":"v1.36.4"}`))
		case r.URL.Path == "/api/v1/namespaces/apps":
			_, _ = w.Write([]byte(`{"kind":"Namespace","apiVersion":"v1","metadata":{"name":"apps","uid":"namespace-identity"}}`))
		case r.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews":
			var review authv1.SelfSubjectAccessReview
			if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			attrs := review.Spec.ResourceAttributes
			review.Status.Allowed = attrs.Subresource != "proxy" || proxyAllowed.Load()
			review.Kind, review.APIVersion = "SelfSubjectAccessReview", "authorization.k8s.io/v1"
			_ = json.NewEncoder(w).Encode(review)
		case strings.Contains(r.URL.Path, "/proxy/"):
			proxied.Store(r.URL.Path)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiserver.Close()
	raw, err := clientcmd.Write(clientapi.Config{CurrentContext: "fixture", Clusters: map[string]*clientapi.Cluster{"fixture": {Server: apiserver.URL, CertificateAuthorityData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: apiserver.Certificate().Raw})}}, AuthInfos: map[string]*clientapi.AuthInfo{"fixture": {Token: "fixture-only-token"}}, Contexts: map[string]*clientapi.Context{"fixture": {Cluster: "fixture", AuthInfo: "fixture", Namespace: "apps"}}})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := kubernetes.ParseConfig(raw, map[string]string{"architecture": "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	client, err := kubernetes.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := client.Check(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	connection := &connections.Connection{ID: "conn", OrgID: "org", Cloud: connections.Kubernetes, Status: connections.StatusReady, Details: checked.Details}

	f := newComponentFixture(t, recipestest.Registry(t))
	f.target.Cloud = connections.Kubernetes
	f.target.Credentials = buildCredentialFunc(func(context.Context, string, string) (*connections.Connection, []byte, error) {
		return connection, raw, nil
	})
	f.secrets.m[resourceKey("org", "env", "app--postgres", "url")] = []byte(postgresURL)
	job := f.componentJob(t, "api", newPostgres)
	job.Action.Provider, job.Connection.Cloud = connections.Kubernetes, connections.Kubernetes
	job.Action.Resource.Provider = connections.Kubernetes
	job.Action.Resource.TargetSettings = planning.ReviewedConnectionSettings(connections.Kubernetes, checked.Details)
	out, err := f.target.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	b := bound(job, out)
	b.Provider = connections.Kubernetes

	if _, _, err = f.target.ComponentTransport(t.Context(), b, 9090); err == nil || !strings.Contains(err.Error(), "service proxy") {
		t.Fatalf("without the service proxy: %v", err)
	}

	proxyAllowed.Store(true)
	transport, closer, err := f.target.ComponentTransport(t.Context(), b, 9090)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://component/admin", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	res, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	iid, _ := id.Parse(out.ExternalID)
	if got, want := proxied.Load(), "/api/v1/namespaces/apps/services/"+kubernetes.WorkloadName(iid)+":9090/proxy/admin"; got != want {
		t.Fatalf("proxied %v, want %s", got, want)
	}
}

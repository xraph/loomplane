package server

import (
	"archive/tar"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

type runtimeTransport func(*http.Request) (*http.Response, error)

func (f runtimeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRuntime_refusesForeignContainerBeforeAnyMutation(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	var err error
	mutations := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutations++
			t.Errorf("foreign object was mutated: %s %s", r.Method, r.URL)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "foreign", "Config": map[string]any{"Labels": map[string]string{owned.LabelInstance: "inst_foreign", owned.LabelOrg: "org_other"}}})
	}))
	defer api.Close()
	u, err := url.Parse(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
		return &Client{Config: &Config{Details: map[string]string{"docker_id": "daemon_fixture", "advertise_host": "server.example.com"}}, HTTP: &http.Client{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
			r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
			return http.DefaultTransport.RoundTrip(r)
		})}}, nil
	}}
	labels := map[string]string{owned.LabelOrg: "org_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelProject: "proj_fixture", owned.LabelConnection: "conn_fixture"}
	_, err = runtime.Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: "sha256:" + strings.Repeat("a", 64)}, labels)
	if err == nil || !strings.Contains(err.Error(), "owned") || mutations != 0 {
		t.Fatalf("ownership: %d %v", mutations, err)
	}
	if err = runtime.Remove(t.Context(), iid, labels); err == nil || mutations != 0 {
		t.Fatalf("foreign cleanup: %d %v", mutations, err)
	}
	for _, running := range []bool{false, true} {
		if err = runtime.SetRunning(t.Context(), iid, labels, running); !errors.Is(err, ErrOwned) || mutations != 0 {
			t.Fatalf("foreign state control: %d %v", mutations, err)
		}
	}
}

func TestRuntime_injectsSecretsThroughDockerAndKeepsPrivateAddress(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	var err error
	var create map[string]any
	created := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json"):
			if !created {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "container_one", "Image": "sha256:" + strings.Repeat("a", 64), "Config": map[string]any{"Labels": map[string]string{owned.LabelInstance: iid.String(), owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture"}}, "State": map[string]any{"Running": true, "Status": "running"}})
		case r.URL.Path == "/networks/"+NetworkName("org_fixture", "env_fixture"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "network_one", "Labels": map[string]string{owned.LabelOrg: "org_fixture", owned.LabelEnvironment: "env_fixture"}})
		case r.Method == http.MethodPost && r.URL.Path == "/containers/create":
			if err = json.NewDecoder(r.Body).Decode(&create); err != nil {
				t.Error(err)
			}
			created = true
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"Id":"container_one"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start"):
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected runtime request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	u, err := url.Parse(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
		return &Client{Config: &Config{Details: map[string]string{"docker_id": "daemon_fixture", "advertise_host": "server.example.com"}}, HTTP: &http.Client{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
			r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
			return http.DefaultTransport.RoundTrip(r)
		})}}, nil
	}, Values: func(context.Context, map[string]string) (owned.RuntimeValues, error) {
		return owned.RuntimeValues{Env: map[string]string{"TOKEN": "private-runtime-token"}}, nil
	}}
	labels := map[string]string{owned.LabelOrg: "org_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelProject: "proj_fixture", owned.LabelConnection: "conn_fixture", owned.LabelRuntime: "vault_reference", owned.LabelPublic: "false"}
	result, err := runtime.Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: "sha256:" + strings.Repeat("a", 64), Env: map[string]string{"MODE": "production"}, Ports: []cpprovider.PortSpec{{Container: 8080}}}, labels)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Endpoints) != 1 || result.Endpoints[0].Public || !strings.Contains(result.Endpoints[0].URL, ContainerName(iid)) {
		t.Fatalf("private endpoint: %+v", result)
	}
	encoded, _ := json.Marshal(create["Env"])
	if !strings.Contains(string(encoded), "TOKEN=private-runtime-token") {
		t.Fatalf("runtime value absent: %s", encoded)
	}
	encoded, _ = json.Marshal(create["Labels"])
	if create["Labels"].(map[string]any)[owned.LabelPublic] != "false" {
		t.Fatalf("container lost its reviewed private identity: %s", encoded)
	}
	if strings.Contains(string(encoded), "private-runtime-token") {
		t.Fatal("runtime secret entered lifecycle labels")
	}
	host, ok := create["HostConfig"].(map[string]any)
	if !ok || host["Init"] != true {
		t.Fatal("managed containers need an init process to reap orphaned children")
	}
}

// The fixture applies a mutation before returning an error, like a lost daemon response.
func TestRuntime_recoversInterruptedReplacement(t *testing.T) {
	for _, failure := range []string{"network", "stop", "stop-unchanged", "create", "backup", "running-backup", "collision"} {
		t.Run(failure, func(t *testing.T) {
			iid := id.New(id.PrefixInstance)
			labels := map[string]string{owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelConnection: "conn_fixture", owned.LabelPublic: "true", owned.LabelHostPort: "18080", owned.LabelRevision: "new"}
			name := ContainerName(iid)
			type item struct {
				id, image, revision string
				running             bool
			}
			containers := map[string]*item{}
			if failure != "network" && failure != "create" {
				containers[name] = &item{"old", "sha256:old", "old", true}
			}
			if failure == "backup" || failure == "running-backup" {
				containers[name+"-previous"] = containers[name]
				delete(containers, name)
				containers[name+"-previous"].running = failure == "running-backup"
			}
			leases := &runtimeLeases{}
			stops := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/containers/json" {
					if failure == "collision" {
						_, _ = io.WriteString(w, `[{"Id":"unrelated","Ports":[{"PublicPort":18080}]}]`)
					} else {
						_, _ = io.WriteString(w, `[]`)
					}
					return
				}
				if strings.HasPrefix(r.URL.Path, "/networks/") {
					if failure == "network" {
						w.WriteHeader(500)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Labels": map[string]string{owned.LabelOrg: "org_fixture", owned.LabelEnvironment: "env_fixture"}})
					return
				}
				if r.URL.Path == "/containers/create" {
					containers[name] = &item{"candidate", "sha256:new", "new", false}
					if failure == "create" {
						w.WriteHeader(500)
						return
					}
					_, _ = io.WriteString(w, `{"Id":"candidate"}`)
					return
				}
				parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/containers/"), "/")
				var current *item
				key := parts[0]
				for k, value := range containers {
					if k == parts[0] || value.id == parts[0] {
						current, key = value, k
						break
					}
				}
				if current == nil {
					http.NotFound(w, r)
					return
				}
				if r.Method == http.MethodDelete {
					delete(containers, key)
					w.WriteHeader(204)
					return
				}
				switch parts[len(parts)-1] {
				case "json":
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": current.id, "Config": map[string]any{"Image": current.image, "Labels": map[string]string{owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelInstance: iid.String(), owned.LabelRevision: current.revision}}, "State": map[string]any{"Running": current.running, "Status": "exited", "ExitCode": 1}})
				case "stop":
					current.running = failure == "stop-unchanged"
					stops++
					if failure == "stop" || failure == "stop-unchanged" {
						w.WriteHeader(500)
					} else {
						w.WriteHeader(204)
					}
				case "rename":
					delete(containers, key)
					containers[r.URL.Query().Get("name")] = current
					w.WriteHeader(204)
				case "start":
					alreadyRunning := current.running
					current.running = true
					if current.id == "candidate" {
						current.running = false
					}
					if alreadyRunning {
						w.WriteHeader(http.StatusNotModified)
					} else {
						w.WriteHeader(http.StatusNoContent)
					}
				default:
					t.Errorf("unexpected %s", r.URL)
				}
			}))
			defer api.Close()
			u, _ := url.Parse(api.URL)
			runtime := &Runtime{Ports: leases, Session: func(context.Context, map[string]string) (*Client, error) {
				return &Client{Config: &Config{Details: map[string]string{"docker_id": "fixture"}}, HTTP: &http.Client{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
					r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
					return http.DefaultTransport.RoundTrip(r)
				})}}, nil
			}}
			_, err := runtime.Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: "sha256:new", Ports: []cpprovider.PortSpec{{Container: 8080}}}, labels)
			if err == nil {
				t.Fatal("failed rollout was accepted")
			}
			if strings.Contains(err.Error(), "HTTP 304") {
				t.Fatalf("already-running recovery failed: %v", err)
			}
			if failure == "network" || failure == "create" {
				if len(containers) != 0 || leases.held {
					t.Fatalf("leaked candidate/lease: %+v %+v", containers, leases)
				}
			} else {
				if containers[name] == nil || containers[name].id != "old" || !containers[name].running || len(containers) != 1 {
					t.Fatalf("prior service not restored: %+v", containers)
				}
			}
			if failure == "collision" && stops != 0 {
				t.Fatal("port conflict stopped the old service")
			}
		})
	}
}

type runtimeLeases struct {
	held  bool
	ports map[int]bool
}

func (r *runtimeLeases) Reserve(_ context.Context, lease owned.Reservation) error {
	r.held = true
	if r.ports != nil {
		r.ports[lease.Port] = true
	}
	return nil
}
func (r *runtimeLeases) Release(_ context.Context, lease owned.Reservation) error {
	r.held = false
	delete(r.ports, lease.Port)
	return nil
}
func (r *runtimeLeases) Reconcile(_ context.Context, lease owned.Reservation) error {
	for port := range r.ports {
		if port != lease.Port {
			delete(r.ports, port)
		}
	}
	r.held = lease.Port > 0
	return nil
}

func TestRuntime_reconcilesLeasesAfterBackupDeletionAndOnRemoval(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	labels := map[string]string{owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelConnection: "conn_fixture", owned.LabelRevision: "new", owned.LabelPublic: "true", owned.LabelHostPort: "18081"}
	leases := &runtimeLeases{ports: map[int]bool{18080: true, 18081: true}}
	present := true
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/containers/json":
			_, _ = io.WriteString(w, `[]`)
		case strings.HasPrefix(r.URL.Path, "/networks/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Labels": map[string]string{owned.LabelOrg: "org_fixture", owned.LabelEnvironment: "env_fixture"}, "Containers": map[string]any{"current": true}})
		case r.URL.Path == "/containers/"+ContainerName(iid)+"/json" && present:
			containerLabels := map[string]string{owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelInstance: iid.String(), owned.LabelRevision: "new", owned.LabelHostPort: "18081"}
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "current", "Config": map[string]any{"Image": "sha256:new", "Labels": containerLabels}, "State": map[string]any{"Running": true}})
		case r.Method == http.MethodDelete && r.URL.Path == "/containers/current":
			present = false
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	u, _ := url.Parse(api.URL)
	runtime := &Runtime{Ports: leases, Session: func(context.Context, map[string]string) (*Client, error) {
		return &Client{Config: &Config{Details: map[string]string{"docker_id": "fixture"}}, HTTP: &http.Client{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
			r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
			return http.DefaultTransport.RoundTrip(r)
		})}}, nil
	}}
	if _, err := runtime.Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: "sha256:new"}, labels); err != nil {
		t.Fatal(err)
	}
	if leases.ports[18080] || !leases.ports[18081] {
		t.Fatalf("recovered reservations: %+v", leases.ports)
	}
	leases.ports[18082] = true // An earlier crash may have lost more than one backup.
	if err := runtime.Remove(t.Context(), iid, labels); err != nil {
		t.Fatal(err)
	}
	if len(leases.ports) != 0 {
		t.Fatalf("orphaned reservations after removal: %+v", leases.ports)
	}
}

func TestRuntime_pullErrorDoesNotExposeBrokeredToken(t *testing.T) {
	auth := base64.URLEncoding.EncodeToString([]byte(`{"registrytoken":"private-broker-token"}`))
	client := &Client{HTTP: &http.Client{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
		status, body := 200, `{"error":"denied private-broker-token"}`
		if r.Method == http.MethodGet {
			status = 404
			body = `{}`
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}}
	if err := (&Runtime{}).pull(t.Context(), client, "registry.example.com/team/api@sha256:fixture", auth); err == nil || strings.Contains(err.Error(), "private-broker-token") {
		t.Fatalf("pull error leaked authentication: %v", err)
	}
}

func TestServerWaitProbesDeclaredHealthPort(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/containers/"+ContainerName(iid)+"/json" {
			t.Errorf("unexpected runtime request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Id":     "container_one",
			"Config": map[string]any{"Labels": map[string]string{owned.LabelInstance: iid.String(), owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture"}},
			"State":  map[string]any{"Running": true, "Status": "running"},
			"NetworkSettings": map[string]any{"Ports": map[string]any{
				"8080/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": "32001"}},
				"9090/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": "32002"}},
			}},
		})
	}))
	defer api.Close()
	u, err := url.Parse(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{Config: &Config{Details: map[string]string{"docker_id": "daemon_fixture"}}, HTTP: &http.Client{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
		return http.DefaultTransport.RoundTrip(r)
	})}}
	labels := map[string]string{owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelHealth: "/readyz", owned.LabelHealthPort: "8080"}
	var probed []int
	answer := false
	runtime := &Runtime{Wait: time.Nanosecond, Probe: func(_ context.Context, _ *Client, port int, path string) error {
		probed = append(probed, port)
		if path != "/readyz" {
			t.Errorf("probed %s", path)
		}
		if answer {
			return nil
		}
		return errors.New("not ready")
	}}
	if err = runtime.wait(t.Context(), client, ContainerName(iid), iid, labels); err == nil {
		t.Fatal("a component that never answered passed readiness")
	}
	if !slices.Equal(probed, []int{32001}) {
		t.Fatalf("probed host ports %v, want only 8080's", probed)
	}
	probed, answer = nil, true
	if err = runtime.wait(t.Context(), client, ContainerName(iid), iid, labels); err != nil || !slices.Equal(probed, []int{32001}) {
		t.Fatalf("ready component: probed %v, %v", probed, err)
	}
	status, err := (&Runtime{Probe: runtime.Probe, Session: func(context.Context, map[string]string) (*Client, error) { return client, nil }}).Status(t.Context(), iid, labels)
	if err != nil || !status.Ready || !slices.Equal(probed, []int{32001, 32001}) {
		t.Fatalf("status: %+v probed %v, %v", status, probed, err)
	}
}

func TestServerUploadsFilesBeforeStart(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	type entry struct {
		dir, name string
		mode      int64
		uid, gid  int
		data      string
	}
	var (
		requests []string
		entries  []entry
		created  bool
	)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json"):
			if !created {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "container_one", "Config": map[string]any{"Labels": map[string]string{owned.LabelInstance: iid.String(), owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture"}}, "State": map[string]any{"Running": true, "Status": "running"}})
		case r.URL.Path == "/networks/"+NetworkName("org_fixture", "env_fixture"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "network_one", "Labels": map[string]string{owned.LabelOrg: "org_fixture", owned.LabelEnvironment: "env_fixture"}})
		case r.Method == http.MethodPost && r.URL.Path == "/containers/create":
			created = true
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"Id":"container_one"}`)
		case r.Method == http.MethodPut && r.URL.Path == "/containers/container_one/archive":
			dir := r.URL.Query().Get("path")
			requests[len(requests)-1] += "?path=" + dir
			// The image has /etc/sample but no /opt/sample.
			if dir != "/etc/sample" && dir != "/" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"message":"Could not find the file in container"}`)
				return
			}
			if r.Header.Get("Content-Type") != "application/x-tar" {
				t.Errorf("archive content type %q", r.Header.Get("Content-Type"))
			}
			archive := tar.NewReader(r.Body)
			for {
				header, err := archive.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Error(err)
					break
				}
				data, _ := io.ReadAll(archive)
				entries = append(entries, entry{dir, header.Name, header.Mode, header.Uid, header.Gid, string(data)})
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start"):
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected runtime request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	u, err := url.Parse(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
		return &Client{Config: &Config{Details: map[string]string{"docker_id": "daemon_fixture"}}, HTTP: &http.Client{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
			r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
			return http.DefaultTransport.RoundTrip(r)
		})}}, nil
	}, Values: func(context.Context, map[string]string) (owned.RuntimeValues, error) {
		return owned.RuntimeValues{
			Files:     map[string][]byte{"/etc/sample/config.yaml": []byte("listen: 8080\n"), "/opt/sample/rules.json": []byte("{}")},
			FileModes: map[string]fs.FileMode{"/opt/sample/rules.json": 0o440},
		}, nil
	}}
	labels := map[string]string{owned.LabelOrg: "org_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelProject: "proj_fixture", owned.LabelConnection: "conn_fixture", owned.LabelRuntime: "vault_reference", owned.LabelRuntimeFiles: "true", owned.LabelPublic: "false"}
	if _, err = runtime.Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: "sha256:" + strings.Repeat("a", 64)}, labels); err != nil {
		t.Fatal(err)
	}
	upload := slices.Index(requests, "PUT /containers/container_one/archive?path=/etc/sample")
	start := slices.Index(requests, "POST /containers/container_one/start")
	if upload < 0 || start < upload {
		t.Fatalf("requests: %q", requests)
	}
	// A directory the image lacks is created from the root, before start.
	if fallback := slices.Index(requests, "PUT /containers/container_one/archive?path=/"); fallback < 0 || start < fallback {
		t.Fatalf("requests: %q", requests)
	}
	want := []entry{
		{"/etc/sample", "config.yaml", 0o444, 0, 0, "listen: 8080\n"},
		{"/", "opt/sample/rules.json", 0o440, 0, 0, "{}"},
	}
	if !slices.Equal(entries, want) {
		t.Fatalf("archive entries: %+v", entries)
	}
}

func TestServerWaitProbesRESP(t *testing.T) {
	if respTimeout != 5*time.Second {
		t.Fatalf("RESP readiness waits %s", respTimeout)
	}
	ping := func(reply string, hang bool) error {
		t.Helper()
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer server.Close()
			request := make([]byte, len("PING\r\n"))
			if _, err := io.ReadFull(server, request); err != nil || string(request) != "PING\r\n" {
				t.Errorf("request %q: %v", request, err)
				return
			}
			if hang {
				_, _ = io.Copy(io.Discard, server)
				return
			}
			_, _ = io.WriteString(server, reply)
		}()
		err := pingRESP(t.Context(), 50*time.Millisecond, func(context.Context) (net.Conn, error) { return client, nil })
		_ = client.Close()
		<-done
		return err
	}
	for _, reply := range []string{"+PONG\r\n", "-NOAUTH Authentication required.\r\n"} {
		if err := ping(reply, false); err != nil {
			t.Fatalf("%q: %v", reply, err)
		}
	}
	for _, reply := range []string{"-LOADING Redis is loading the dataset in memory\r\n", "", "+OK\r\n", "-ERR unknown\r\n"} {
		if err := ping(reply, false); err == nil {
			t.Fatalf("%q passed readiness", reply)
		}
	}
	if err := ping("", true); err == nil {
		t.Fatal("a silent port passed readiness")
	}

	iid := id.New(id.PrefixInstance)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Id":     "container_one",
			"Config": map[string]any{"Labels": map[string]string{owned.LabelInstance: iid.String(), owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture"}},
			"State":  map[string]any{"Running": true, "Status": "running"},
			"NetworkSettings": map[string]any{"Ports": map[string]any{
				"8080/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": "32001"}},
				"6379/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": "32002"}},
			}},
		})
	}))
	defer api.Close()
	u, err := url.Parse(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{Config: &Config{Details: map[string]string{"docker_id": "daemon_fixture"}}, HTTP: &http.Client{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
		return http.DefaultTransport.RoundTrip(r)
	})}}
	labels := map[string]string{owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelHealthPort: "6379", owned.LabelHealthProtocol: "redis"}
	var probed []int
	answer := false
	runtime := &Runtime{Wait: time.Nanosecond, Probe: func(context.Context, *Client, int, string) error {
		t.Error("probed a RESP port over HTTP")
		return nil
	}, ProbeRESP: func(_ context.Context, _ *Client, port int) error {
		probed = append(probed, port)
		if answer {
			return nil
		}
		return errors.New("-LOADING")
	}}
	if err = runtime.wait(t.Context(), client, ContainerName(iid), iid, labels); err == nil {
		t.Fatal("a loading component passed readiness")
	}
	probed, answer = nil, true
	if err = runtime.wait(t.Context(), client, ContainerName(iid), iid, labels); err != nil || !slices.Equal(probed, []int{32002}) {
		t.Fatalf("ready component: probed %v, %v", probed, err)
	}
	status, err := (&Runtime{ProbeRESP: runtime.ProbeRESP, Session: func(context.Context, map[string]string) (*Client, error) { return client, nil }}).Status(t.Context(), iid, labels)
	if err != nil || !status.Ready || !slices.Equal(probed, []int{32002, 32002}) {
		t.Fatalf("status: %+v probed %v, %v", status, probed, err)
	}
	labels[owned.LabelHealthProtocol] = "gopher"
	if err = runtime.wait(t.Context(), client, ContainerName(iid), iid, labels); err == nil {
		t.Fatal("an unknown readiness protocol passed")
	}
}

func TestComponentsPublishLoopbackAndJoinTheEnvironmentNetwork(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	var create struct {
		HostConfig struct {
			PortBindings map[string][]map[string]string
		}
		NetworkingConfig struct {
			EndpointsConfig map[string]struct{ Aliases []string }
		}
	}
	created := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json"):
			if !created {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "container_one", "Config": map[string]any{"Labels": map[string]string{owned.LabelInstance: iid.String(), owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture"}}, "State": map[string]any{"Running": true, "Status": "running"}})
		case r.URL.Path == "/networks/"+NetworkName("org_fixture", "env_fixture"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "network_one", "Labels": map[string]string{owned.LabelOrg: "org_fixture", owned.LabelEnvironment: "env_fixture"}})
		case r.Method == http.MethodPost && r.URL.Path == "/containers/create":
			if err := json.NewDecoder(r.Body).Decode(&create); err != nil {
				t.Error(err)
			}
			created = true
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"Id":"container_one"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start"):
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected runtime request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	u, err := url.Parse(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
		return &Client{Config: &Config{Details: map[string]string{"docker_id": "daemon_fixture"}}, HTTP: &http.Client{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
			r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
			return http.DefaultTransport.RoundTrip(r)
		})}}, nil
	}}
	labels := map[string]string{owned.LabelOrg: "org_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelProject: "proj_fixture", owned.LabelConnection: "conn_fixture", owned.LabelPublic: "false", owned.LabelCatalogKind: "sample", owned.LabelCatalogResource: "app", owned.LabelCatalogComponent: "gateway", owned.LabelKubernetesRules: `[{"api_groups":[""],"resources":["services"],"verbs":["get"]}]`}
	spec := cpprovider.ServiceSpec{Name: "gateway", Image: "sha256:" + strings.Repeat("a", 64), Ports: []cpprovider.PortSpec{{Container: 8080, Protocol: "tcp"}, {Container: 9090, Protocol: "tcp"}}}
	if _, err = runtime.Apply(t.Context(), iid, spec, labels); err != nil {
		t.Fatal(err)
	}
	loopback := []map[string]string{{"HostIp": "127.0.0.1", "HostPort": ""}}
	if len(create.HostConfig.PortBindings) != 2 || !reflect.DeepEqual(create.HostConfig.PortBindings["8080/tcp"], loopback) || !reflect.DeepEqual(create.HostConfig.PortBindings["9090/tcp"], loopback) {
		t.Fatalf("port bindings: %+v", create.HostConfig.PortBindings)
	}
	endpoints := create.NetworkingConfig.EndpointsConfig
	network, ok := endpoints[NetworkName("org_fixture", "env_fixture")]
	if len(endpoints) != 1 || !ok || !slices.Equal(network.Aliases, []string{ContainerName(iid)}) {
		t.Fatalf("networks: %+v", endpoints)
	}
}

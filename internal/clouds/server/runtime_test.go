package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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
	for _, failure := range []string{"network", "stop", "create", "backup", "collision"} {
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
			if failure == "backup" {
				containers[name+"-previous"] = containers[name]
				delete(containers, name)
				containers[name+"-previous"].running = false
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
					current.running = false
					stops++
					if failure == "stop" {
						w.WriteHeader(500)
					} else {
						w.WriteHeader(204)
					}
				case "rename":
					delete(containers, key)
					containers[r.URL.Query().Get("name")] = current
					w.WriteHeader(204)
				case "start":
					current.running = true
					if current.id == "candidate" {
						current.running = false
					}
					w.WriteHeader(204)
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

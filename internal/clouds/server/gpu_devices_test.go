package server

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestAssignDevices(t *testing.T) {
	devs := []provider.GPUDevice{{Index: 0, UUID: "GPU-a", Kind: "nvidia-l4"}, {Index: 1, UUID: "GPU-b", Kind: "nvidia-l4"}, {Index: 2, UUID: "GPU-c", Kind: "nvidia-t4"}}
	if got, err := assignDevices(devs, "nvidia-l4", 1, map[string]string{"GPU-a": "lp-other"}, nil); err != nil || !slices.Equal(got, []string{"GPU-b"}) {
		t.Fatalf("free device: %v %v", got, err)
	}
	if got, err := assignDevices(devs, "nvidia-l4", 1, nil, []string{"GPU-b"}); err != nil || !slices.Equal(got, []string{"GPU-b"}) {
		t.Fatalf("rollover keeps its device: %v %v", got, err)
	}
	if _, err := assignDevices(devs, "nvidia-l4", 2, map[string]string{"GPU-a": "lp-other"}, nil); !errors.Is(err, ErrGPUBusy) || !strings.Contains(err.Error(), "lp-other") {
		t.Fatalf("busy: %v", err)
	}
	if got, err := assignDevices(devs, "nvidia-l4", 2, nil, nil); err != nil || !slices.Equal(got, []string{"GPU-a", "GPU-b"}) {
		t.Fatalf("lowest free indexes: %v %v", got, err)
	}
	if got, err := assignDevices(devs, "nvidia-l4", 1, map[string]string{"GPU-b": "lp-other"}, []string{"GPU-b"}); err != nil || !slices.Equal(got, []string{"GPU-a"}) {
		t.Fatalf("a previous device another workload took isn't kept: %v %v", got, err)
	}
	if got, err := assignDevices(devs, "nvidia-l4", 1, nil, []string{"GPU-gone"}); err != nil || !slices.Equal(got, []string{"GPU-a"}) {
		t.Fatalf("a previous device the server no longer has isn't kept: %v %v", got, err)
	}
	if got, err := assignDevices(devs, "nvidia-l4", 1, nil, []string{"GPU-c"}); err != nil || !slices.Equal(got, []string{"GPU-a"}) {
		t.Fatalf("a previous device of another kind isn't kept: %v %v", got, err)
	}
	_, err := assignDevices(devs, "nvidia-l4", 2, map[string]string{"GPU-a": "lp-other"}, nil)
	if want := "GPU 0 on this server is in use by lp-other; 1 free NVIDIA L4 GPUs and this workload needs 2."; err == nil || err.Error() != want {
		t.Fatalf("busy message: %v", err)
	}
	if _, err := assignDevices(devs, "nvidia-l4", 3, nil, nil); err == nil || errors.Is(err, ErrGPUBusy) || !strings.Contains(err.Error(), "has 2 NVIDIA L4 GPUs") {
		t.Fatalf("a server with too few devices isn't busy, it's short: %v", err)
	}
}

// gpuDocker is a fake Docker Engine API that keeps containers by name.
type gpuDocker struct {
	t          *testing.T
	mu         sync.Mutex
	containers map[string]*gpuContainer
	creates    []map[string]any
	lists      int
}

type gpuContainer struct {
	id      string
	image   string
	labels  map[string]string
	running bool
}

func (d *gpuDocker) find(key string) (string, *gpuContainer) {
	for name, c := range d.containers {
		if name == key || c.id == key {
			return name, c
		}
	}
	return "", nil
}

func (d *gpuDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/containers/json":
		if r.URL.Query().Get("all") != "1" {
			d.t.Errorf("container list must include stopped containers: %s", r.URL)
		}
		d.lists++
		out := make([]map[string]any, 0, len(d.containers))
		for _, name := range slices.Sorted(maps.Keys(d.containers)) {
			c := d.containers[name]
			state := "exited"
			if c.running {
				state = "running"
			}
			out = append(out, map[string]any{"Id": c.id, "Names": []string{"/" + name}, "Labels": c.labels, "State": state})
		}
		_ = json.NewEncoder(w).Encode(out)
	case strings.HasPrefix(path, "/networks/"):
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "network_one", "Labels": map[string]string{owned.LabelOrg: "org_fixture", owned.LabelEnvironment: "env_fixture"}})
	case r.Method == http.MethodPost && path == "/containers/create":
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			d.t.Error(err)
		}
		d.creates = append(d.creates, body)
		labels := map[string]string{}
		for k, v := range body["Labels"].(map[string]any) {
			labels[k] = v.(string)
		}
		name := r.URL.Query().Get("name")
		d.containers[name] = &gpuContainer{id: "created_" + name, image: body["Image"].(string), labels: labels}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": "created_" + name})
	case strings.HasPrefix(path, "/containers/"):
		parts := strings.Split(strings.TrimPrefix(path, "/containers/"), "/")
		name, c := d.find(parts[0])
		if c == nil {
			http.NotFound(w, r)
			return
		}
		switch {
		case r.Method == http.MethodGet && len(parts) == 2 && parts[1] == "json":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": c.id, "Image": c.image, "Config": map[string]any{"Image": c.image, "Labels": c.labels}, "State": map[string]any{"Running": c.running, "Status": map[bool]string{true: "running", false: "exited"}[c.running]}})
		case r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "start":
			c.running = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "stop":
			c.running = false
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "rename":
			delete(d.containers, name)
			d.containers[r.URL.Query().Get("name")] = c
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && len(parts) == 1:
			delete(d.containers, name)
			w.WriteHeader(http.StatusNoContent)
		default:
			d.t.Errorf("unexpected container request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	default:
		d.t.Errorf("unexpected runtime request: %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}
}

// gpuRuntime runs a Runtime against d whose server has the given GPUs.
func gpuRuntime(t *testing.T, d *gpuDocker, inv provider.GPUInventory, reads *int) *Runtime {
	t.Helper()
	api := httptest.NewServer(d)
	t.Cleanup(api.Close)
	u, err := url.Parse(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &Runtime{
		Session: func(context.Context, map[string]string) (*Client, error) {
			return &Client{Config: &Config{Details: map[string]string{"docker_id": "daemon_fixture"}}, HTTP: &http.Client{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
				r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
				return http.DefaultTransport.RoundTrip(r)
			})}}, nil
		},
		GPUReader: func(context.Context, *Client) (provider.GPUInventory, error) {
			if reads != nil {
				*reads++
			}
			return inv, nil
		},
	}
}

func twoL4s() provider.GPUInventory {
	return provider.GPUInventory{Status: provider.GPUStatusReady, Devices: []provider.GPUDevice{
		{Index: 0, UUID: "GPU-a", Kind: "nvidia-l4", Product: "NVIDIA L4", MemoryMiB: 23034},
		{Index: 1, UUID: "GPU-b", Kind: "nvidia-l4", Product: "NVIDIA L4", MemoryMiB: 23034},
	}}
}

func gpuLabels(revision string) map[string]string {
	return map[string]string{
		owned.LabelOrg: "org_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelProject: "proj_fixture", owned.LabelConnection: "conn_fixture",
		owned.LabelPublic: "false", owned.LabelRevision: revision,
		owned.LabelGPU: `{"kind":"nvidia-l4","count":1}`,
	}
}

func foreign(devices string) *gpuContainer {
	return &gpuContainer{id: "foreign_" + devices, image: "sha256:other", running: true, labels: map[string]string{owned.LabelInstance: "inst_other", LabelGPUDevices: devices}}
}

var gpuImage = "sha256:" + strings.Repeat("a", 64)

func TestApplyRequestsAssignedGPUsAndSharedMemory(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	d := &gpuDocker{t: t, containers: map[string]*gpuContainer{"lp-other": foreign("GPU-a")}}
	runtime := gpuRuntime(t, d, twoL4s(), nil)
	labels := gpuLabels("first")
	labels[owned.LabelShmMB] = "2048"
	if _, err := runtime.Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: gpuImage}, labels); err != nil {
		t.Fatal(err)
	}
	if len(d.creates) != 1 {
		t.Fatalf("creates: %d", len(d.creates))
	}
	create := d.creates[0]
	host := create["HostConfig"].(map[string]any)
	requests := host["DeviceRequests"].([]any)[0].(map[string]any)
	if requests["Driver"] != "nvidia" || !reflect.DeepEqual(requests["DeviceIDs"], []any{"GPU-b"}) || !reflect.DeepEqual(requests["Capabilities"], []any{[]any{"gpu"}}) {
		t.Fatalf("device request: %+v", requests)
	}
	if host["ShmSize"] != float64(2048<<20) || create["Labels"].(map[string]any)[LabelGPUDevices] != "GPU-b" {
		t.Fatalf("shm or device label: %+v", create)
	}
	if create["Labels"].(map[string]any)[owned.LabelGPU] != `{"kind":"nvidia-l4","count":1}` || create["Labels"].(map[string]any)[owned.LabelShmMB] != "2048" {
		t.Fatalf("container lost its reviewed GPU placement: %+v", create["Labels"])
	}
}

func TestApplyRefusesHeldDevices(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	d := &gpuDocker{t: t, containers: map[string]*gpuContainer{"lp-one": foreign("GPU-a"), "lp-two": foreign("GPU-b")}}
	d.containers["lp-two"].running = false // a stopped container still holds its devices
	runtime := gpuRuntime(t, d, twoL4s(), nil)
	_, err := runtime.Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: gpuImage}, gpuLabels("first"))
	if !errors.Is(err, ErrGPUBusy) || !strings.Contains(err.Error(), "GPU 0 on this server is in use by lp-one; 0 free NVIDIA L4 GPUs and this workload needs 1.") {
		t.Fatalf("busy: %v", err)
	}
	if len(d.creates) != 0 {
		t.Fatalf("a busy GPU still created a container: %+v", d.creates)
	}
}

// The planner counts a re-planned workload's own GPUs as free, so a
// rollover on a full server has to reuse what its instance already holds.
func TestApplyReusesItsOwnDevicesOnReplace(t *testing.T) {
	oneL4 := provider.GPUInventory{Status: provider.GPUStatusReady, Devices: []provider.GPUDevice{{Index: 0, UUID: "GPU-a", Kind: "nvidia-l4", Product: "NVIDIA L4"}}}
	setup := func(t *testing.T, iid id.ID) (*gpuDocker, map[string]string) {
		labels := gpuLabels("new")
		own := map[string]string{owned.LabelInstance: iid.String(), owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelRevision: "old", LabelGPUDevices: "GPU-a", owned.LabelGPU: labels[owned.LabelGPU]}
		return &gpuDocker{t: t, containers: map[string]*gpuContainer{ContainerName(iid): {id: "old", image: "sha256:old", labels: own, running: true}}}, labels
	}

	t.Run("own device", func(t *testing.T) {
		iid := id.New(id.PrefixInstance)
		d, labels := setup(t, iid)
		if _, err := gpuRuntime(t, d, oneL4, nil).Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: gpuImage}, labels); err != nil {
			t.Fatal(err)
		}
		if len(d.creates) != 1 {
			t.Fatalf("creates: %d", len(d.creates))
		}
		requests := d.creates[0]["HostConfig"].(map[string]any)["DeviceRequests"].([]any)[0].(map[string]any)
		if !reflect.DeepEqual(requests["DeviceIDs"], []any{"GPU-a"}) {
			t.Fatalf("rollover didn't reuse its own device: %+v", requests)
		}
		if _, old := d.find("old"); old != nil {
			t.Fatal("the replaced container was kept")
		}
		if _, current := d.find(ContainerName(iid)); current == nil || !current.running || current.labels[LabelGPUDevices] != "GPU-a" {
			t.Fatalf("current container: %+v", current)
		}
	})

	t.Run("own previous container", func(t *testing.T) {
		iid := id.New(id.PrefixInstance)
		d, labels := setup(t, iid)
		// An interrupted rollover left the old container stopped under its backup name.
		d.containers[ContainerName(iid)+"-previous-stopped"] = d.containers[ContainerName(iid)]
		delete(d.containers, ContainerName(iid))
		d.containers[ContainerName(iid)+"-previous-stopped"].running = false
		if _, err := gpuRuntime(t, d, oneL4, nil).Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: gpuImage}, labels); err != nil {
			t.Fatal(err)
		}
		requests := d.creates[0]["HostConfig"].(map[string]any)["DeviceRequests"].([]any)[0].(map[string]any)
		if !reflect.DeepEqual(requests["DeviceIDs"], []any{"GPU-a"}) {
			t.Fatalf("rollover didn't reuse its backup's device: %+v", requests)
		}
	})

	t.Run("another workload's device", func(t *testing.T) {
		iid := id.New(id.PrefixInstance)
		d, labels := setup(t, iid)
		delete(d.containers[ContainerName(iid)].labels, LabelGPUDevices)
		d.containers["lp-other"] = foreign("GPU-a")
		_, err := gpuRuntime(t, d, oneL4, nil).Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: gpuImage}, labels)
		if !errors.Is(err, ErrGPUBusy) || !strings.Contains(err.Error(), "in use by lp-other") {
			t.Fatalf("busy: %v", err)
		}
		if len(d.creates) != 0 {
			t.Fatalf("a busy GPU still created a container: %+v", d.creates)
		}
		if name, old := d.find("old"); old == nil || name != ContainerName(iid) || !old.running {
			t.Fatalf("a refused rollover disturbed the running container: %s %+v", name, old)
		}
	})
}

func TestApplyRefusesUnreadyGPUs(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	d := &gpuDocker{t: t, containers: map[string]*gpuContainer{}}
	inv := provider.GPUInventory{Status: provider.GPUStatusToolkitMissing, Message: "This server has 2 NVIDIA L4 GPUs but Docker can't use them."}
	_, err := gpuRuntime(t, d, inv, nil).Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: gpuImage}, gpuLabels("first"))
	if err == nil || !strings.Contains(err.Error(), "Docker can't use them") {
		t.Fatalf("unready GPUs: %v", err)
	}
	if len(d.creates) != 0 {
		t.Fatal("an unready server still created a container")
	}
}

// Device assignments come only from the runtime, never from the labels
// it's handed, and a workload without GPUs costs no extra calls.
func TestApplyWithoutGPUsIgnoresDeviceLabels(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	d := &gpuDocker{t: t, containers: map[string]*gpuContainer{}}
	reads := 0
	labels := gpuLabels("first")
	delete(labels, owned.LabelGPU)
	labels[LabelGPUDevices] = "GPU-a"
	if _, err := gpuRuntime(t, d, twoL4s(), &reads).Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: gpuImage}, labels); err != nil {
		t.Fatal(err)
	}
	create := d.creates[0]
	if _, ok := create["Labels"].(map[string]any)[LabelGPUDevices]; ok {
		t.Fatalf("input device label reached the container: %+v", create["Labels"])
	}
	host := create["HostConfig"].(map[string]any)
	if _, ok := host["DeviceRequests"]; ok {
		t.Fatalf("device request without a GPU placement: %+v", host)
	}
	if _, ok := host["ShmSize"]; ok {
		t.Fatalf("shared memory without a size: %+v", host)
	}
	if reads != 0 || d.lists != 0 {
		t.Fatalf("GPU reads %d, container lists %d", reads, d.lists)
	}
}

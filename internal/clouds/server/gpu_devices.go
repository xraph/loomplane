package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/xraph/loomplane/pkg/provider"
)

// LabelGPUDevices is the comma-separated device UUIDs a container was
// given. Only the runtime sets it, from the devices it assigned.
const LabelGPUDevices = "loomplane.gpu.devices"

// ErrGPUBusy is a GPU another Loomplane container on the server holds.
var ErrGPUBusy = errors.New("GPU in use")

type gpuBusyError struct{ message string }

func (e *gpuBusyError) Error() string { return e.message }
func (e *gpuBusyError) Unwrap() error { return ErrGPUBusy }

// assignDevices picks count devices of kind for one container. It keeps previous
// when those devices still exist and aren't held, else takes the lowest free indexes.
func assignDevices(devices []provider.GPUDevice, kind string, count int, held map[string]string, previous []string) ([]string, error) {
	if count < 1 {
		return nil, errors.New("a GPU workload needs at least one device")
	}
	title := kind
	if k, ok := provider.LookupAcceleratorKind(kind); ok {
		title = k.Title
	}
	var candidates, free []provider.GPUDevice
	for _, d := range devices {
		if d.Kind != kind {
			continue
		}
		candidates = append(candidates, d)
		if _, taken := held[d.UUID]; !taken {
			free = append(free, d)
		}
	}
	slices.SortFunc(candidates, func(a, b provider.GPUDevice) int { return a.Index - b.Index })
	slices.SortFunc(free, func(a, b provider.GPUDevice) int { return a.Index - b.Index })
	if len(candidates) < count {
		return nil, &provider.GPUUnavailableError{Code: provider.GPUCountUnavailable, Message: fmt.Sprintf("This server has %d %s GPUs and this workload needs %d.", len(candidates), title, count)}
	}
	if len(free) < count {
		i := slices.IndexFunc(candidates, func(d provider.GPUDevice) bool { _, taken := held[d.UUID]; return taken })
		return nil, &gpuBusyError{fmt.Sprintf("GPU %d on this server is in use by %s; %d free %s GPUs and this workload needs %d.", candidates[i].Index, held[candidates[i].UUID], len(free), title, count)}
	}
	chosen := make([]provider.GPUDevice, 0, count)
	for _, d := range free {
		if len(chosen) < count && slices.Contains(previous, d.UUID) {
			chosen = append(chosen, d)
		}
	}
	for _, d := range free {
		if len(chosen) < count && !slices.ContainsFunc(chosen, func(c provider.GPUDevice) bool { return c.UUID == d.UUID }) {
			chosen = append(chosen, d)
		}
	}
	slices.SortFunc(chosen, func(a, b provider.GPUDevice) int { return a.Index - b.Index })
	out := make([]string, len(chosen))
	for i, d := range chosen {
		out[i] = d.UUID
	}
	return out, nil
}

// GPULocks serializes GPU device picking on one Docker daemon. LockGPUs
// blocks until no other apply holds daemon's devices and returns the
// function that lets the next one in. The holder must not wait on any other
// lock until it calls that function.
type GPULocks interface {
	LockGPUs(ctx context.Context, daemon string) (unlock func(), err error)
}

// localGPULocks serializes device picking within this process, for a
// Runtime without GPULocks.
var localGPULocks sync.Map // daemon -> *sync.Mutex

// lockGPUs takes the daemon's GPU lock for the stretch from reading which
// devices are free to creating the container that records them. The
// unlock it returns is safe to call more than once.
func (r *Runtime) lockGPUs(ctx context.Context, c *Client) (func(), error) {
	daemon := c.Config.Details["docker_id"]
	if daemon == "" {
		return nil, errors.New("the server's Docker identity must be verified before Loomplane assigns its GPUs")
	}
	var once sync.Once
	if r.GPULocks == nil {
		value, _ := localGPULocks.LoadOrStore(daemon, &sync.Mutex{})
		mu, ok := value.(*sync.Mutex)
		if !ok {
			return nil, errors.New("the server's local GPU lock is unreadable")
		}
		mu.Lock()
		return func() { once.Do(mu.Unlock) }, nil
	}
	unlock, err := r.GPULocks.LockGPUs(ctx, daemon)
	if err != nil {
		return nil, err
	}
	return func() { once.Do(unlock) }, nil
}

// gpuDevices assigns the devices for a workload's reviewed GPU placement
// from the server's live GPUs. Devices other Loomplane containers hold, in
// any state, aren't free; the instance's own containers, listed in own by
// ID, hold nothing, so a rollover can take back what current had.
func (r *Runtime) gpuDevices(ctx context.Context, c *Client, raw string, own map[string]bool, current *containerInfo) ([]string, error) {
	var placement provider.GPUPlacement
	if err := json.Unmarshal([]byte(raw), &placement); err != nil || placement.Kind == "" || placement.Count < 1 {
		return nil, errors.New("the workload's GPU placement is unreadable")
	}
	read := r.GPUReader
	if read == nil {
		read = func(ctx context.Context, c *Client) (provider.GPUInventory, error) { return c.GPUs(ctx) }
	}
	inv, err := read(ctx, c)
	if err != nil {
		return nil, err
	}
	if inv.Status != provider.GPUStatusReady {
		return nil, errors.New(cmp.Or(inv.Message, gpuUnreadable))
	}
	var containers []struct {
		ID     string `json:"Id"`
		Names  []string
		Labels map[string]string
	}
	if listErr := dockerJSON(ctx, c, http.MethodGet, "/containers/json?all=1", nil, &containers); listErr != nil {
		return nil, listErr
	}
	held := map[string]string{}
	for _, container := range containers {
		if own[container.ID] {
			continue
		}
		name := container.ID
		if len(container.Names) > 0 {
			name = strings.TrimPrefix(container.Names[0], "/")
		}
		for _, uuid := range splitDevices(container.Labels[LabelGPUDevices]) {
			held[uuid] = name
		}
	}
	var previous []string
	if current != nil {
		previous = splitDevices(current.Config.Labels[LabelGPUDevices])
	}
	return assignDevices(inv.Devices, placement.Kind, placement.Count, held, previous)
}

func splitDevices(label string) []string {
	var out []string
	for _, uuid := range strings.Split(label, ",") {
		if uuid = strings.TrimSpace(uuid); uuid != "" {
			out = append(out, uuid)
		}
	}
	return out
}

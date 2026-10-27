package gpus

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestTargetsListConnectionsAndCreatableSizes(t *testing.T) {
	svc := NewService(Deps{Connections: fakeConnections(serverWithTwoL4("gpu-box"), clusterWithL4Nodes("gke"), digitalOceanAccount("do")),
		Bindings: fakeBindings(map[string]map[string]int{"gpu-box": {"nvidia-l4": 1}}),
		Options:  fakeOptions("do", h100Size(3.39)), Authz: allowAll{}})
	list, err := svc.Targets(t.Context(), principal(), GPUTargetQuery{MinMemoryGiB: 20, Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	box := find(list, "gpu-box")
	if box.Source != "connection" || box.Free != 1 || !box.Fits || box.Accelerator.Kind != "nvidia-l4" {
		t.Fatalf("server: %+v", box)
	}
	gke := find(list, "gke")
	if !gke.Fits || !gke.PriceEstimate || !gke.HourlyPriceKnown {
		t.Fatalf("cluster: %+v", gke)
	}
	size := findSize(list, "gpu-h100x1-80gb")
	if size.Source != "server_size" || size.ConnectionID != "do" || size.HourlyUSD != 3.39 || !size.Fits {
		t.Fatalf("size: %+v", size)
	}
	if list, _ = svc.Targets(t.Context(), principal(), GPUTargetQuery{MinMemoryGiB: 40, Count: 1}); find(list, "gpu-box").Fits {
		t.Fatal("a 24 GiB L4 doesn't fit a 40 GiB floor")
	}
}

// A cluster row's Free is the most GPUs one node allocates, however many
// nodes the group has: two one-L4 nodes show 1, not 2, and no workload
// is subtracted from it.
func TestTargetsClusterFreeIsPerNode(t *testing.T) {
	svc := NewService(Deps{Connections: fakeConnections(clusterWithL4Nodes("eks")),
		Bindings: fakeBindings(map[string]map[string]int{"eks": {"nvidia-l4": 1}}), Authz: allowAll{}})
	list, err := svc.Targets(t.Context(), principal(), GPUTargetQuery{Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	if gke := find(list, "eks"); gke.Free != 1 || gke.Accelerator.Count != 1 || !gke.Fits {
		t.Fatalf("cluster: %+v", gke)
	}
}

func TestTargetsSkipUnreadyServers(t *testing.T) {
	svc := NewService(Deps{Connections: fakeConnections(serverWithToolkitMissing("new-droplet")), Bindings: fakeBindings(nil), Options: fakeOptions(""), Authz: allowAll{}})
	list, _ := svc.Targets(t.Context(), principal(), GPUTargetQuery{Count: 1})
	row := find(list, "new-droplet")
	if row.Fits || row.Code != provider.GPUUnavailable || !strings.Contains(row.Message, "NVIDIA Container Toolkit") {
		t.Fatalf("unready server offered: %+v", row)
	}
}

// A GPU server whose toolkit never came up is listed with its reason and
// the price it runs up, so people can see what it costs, and never fits.
func TestTargetsListUnreadyServerWithItsPrice(t *testing.T) {
	servers := fakeServers(&cloudinfra.Server{ID: "srv", RuntimeConnectionID: "new-droplet", Ownership: "managed", Status: "ready", HourlyUSD: 0.95, HourlyPriceKnown: true})
	svc := NewService(Deps{Connections: fakeConnections(serverWithToolkitMissing("new-droplet")), Bindings: fakeBindings(nil), Servers: servers, Authz: allowAll{}})
	list, err := svc.Targets(t.Context(), principal(), GPUTargetQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Targets) != 1 {
		t.Fatalf("rows: %+v", list.Targets)
	}
	row := list.Targets[0]
	if row.Fits || row.Free != 0 || row.Accelerator.Kind != "nvidia-l4" || row.Accelerator.Count != 1 || row.HourlyUSD != 0.95 || !row.HourlyPriceKnown {
		t.Fatalf("unready server: %+v", row)
	}
}

func TestTargetsReportUnreachableAccounts(t *testing.T) {
	// The DigitalOcean options reader fails: the list still returns, with one UnreachableAccount naming "do".
	svc := NewService(Deps{Connections: fakeConnections(serverWithTwoL4("gpu-box"), digitalOceanAccount("do"), digitalOceanAccount("team")), Bindings: fakeBindings(nil),
		Options: failingOptions{err: errors.New("GET https://api.digitalocean.com/v2/sizes: 429 Too Many Requests\nretry later"), ok: fakeOptions("team", h100Size(3.39))}, Authz: allowAll{}})
	list, err := svc.Targets(t.Context(), principal(), GPUTargetQuery{Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Unreachable) != 1 || list.Unreachable[0].ConnectionID != "do" || list.Unreachable[0].Name != "do" {
		t.Fatalf("unreachable: %+v", list.Unreachable)
	}
	if msg := list.Unreachable[0].Message; msg == "" || strings.Contains(msg, "\n") || !strings.Contains(msg, "429") {
		t.Fatalf("reason isn't one line: %q", msg)
	}
	if !find(list, "gpu-box").Fits || findSize(list, "gpu-h100x1-80gb").ConnectionID != "team" {
		t.Fatalf("an unreachable account hid the others: %+v", list.Targets)
	}
}

func TestTargetsNeedInfrastructureRead(t *testing.T) {
	// Authz denies infrastructure:read: Targets returns security.ErrForbidden.
	cons := fakeConnections(serverWithTwoL4("gpu-box"))
	svc := NewService(Deps{Connections: cons, Bindings: fakeBindings(nil), Authz: denyAuthz{security.InfrastructureRead}})
	if _, err := svc.Targets(t.Context(), principal(), GPUTargetQuery{}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("err = %v", err)
	}
	if cons.lists != 0 {
		t.Fatal("connections listed without infrastructure:read")
	}
}

// Listing connections needs cloud_account:read, so the list needs both.
func TestTargetsNeedCloudAccountRead(t *testing.T) {
	cons := fakeConnections(serverWithTwoL4("gpu-box"))
	svc := NewService(Deps{Connections: cons, Bindings: fakeBindings(nil), Authz: denyAuthz{security.CloudAccountRead}})
	if _, err := svc.Targets(t.Context(), principal(), GPUTargetQuery{}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("err = %v", err)
	}
	if cons.lists != 0 {
		t.Fatal("connections listed without cloud_account:read")
	}
}

func TestTargetsRejectBadQueries(t *testing.T) {
	svc := NewService(Deps{Connections: fakeConnections(), Bindings: fakeBindings(nil), Authz: allowAll{}})
	for _, q := range []GPUTargetQuery{{Count: -1}, {Count: 9}, {MinMemoryGiB: -1}} {
		if _, err := svc.Targets(t.Context(), principal(), q); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: err = %v", q, err)
		}
	}
}

// Count defaults to one, and a server's GPUs given to other services aren't free.
func TestTargetsCountHeldGPUs(t *testing.T) {
	svc := NewService(Deps{Connections: fakeConnections(serverWithTwoL4("gpu-box")),
		Bindings: fakeBindings(map[string]map[string]int{"gpu-box": {"nvidia-l4": 2}}), Authz: allowAll{}})
	list, err := svc.Targets(t.Context(), principal(), GPUTargetQuery{})
	if err != nil {
		t.Fatal(err)
	}
	box := find(list, "gpu-box")
	if box.Fits || box.Free != 0 || box.Accelerator.Count != 2 || box.Code != provider.GPUCountUnavailable {
		t.Fatalf("full server: %+v", box)
	}
	svc = NewService(Deps{Connections: fakeConnections(serverWithTwoL4("gpu-box")), Bindings: fakeBindings(nil), Authz: allowAll{}})
	if list, _ = svc.Targets(t.Context(), principal(), GPUTargetQuery{Count: 3}); find(list, "gpu-box").Fits || find(list, "gpu-box").Code != provider.GPUCountUnavailable {
		t.Fatalf("three on a two-GPU server: %+v", find(list, "gpu-box"))
	}
}

// Unknown capacity is not zero usage: a connection whose inventory was
// never read, or doesn't decode, is listed and never fits.
func TestTargetsNeverFitUnknownInventory(t *testing.T) {
	unread := &connections.Connection{ID: "unread", Name: "unread", Cloud: connections.Server, Status: connections.StatusReady, Details: map[string]string{provider.GPUDetailStatus: provider.GPUStatusUnknown}}
	garbled := &connections.Connection{ID: "garbled", Name: "garbled", Cloud: connections.Server, Status: connections.StatusReady, Details: map[string]string{provider.GPUDetailStatus: provider.GPUStatusReady, provider.GPUDetailInventory: "{"}}
	plain := &connections.Connection{ID: "plain", Name: "plain", Cloud: connections.Server, Status: connections.StatusReady, Details: map[string]string{}}
	none := &connections.Connection{ID: "none", Name: "none", Cloud: connections.Server, Status: connections.StatusReady, Details: provider.GPUInventory{Status: provider.GPUStatusNone}.Details()}
	svc := NewService(Deps{Connections: fakeConnections(unread, garbled, plain, none), Bindings: fakeBindings(nil), Authz: allowAll{}})
	list, err := svc.Targets(t.Context(), principal(), GPUTargetQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Targets) != 2 {
		t.Fatalf("rows: %+v", list.Targets)
	}
	for _, name := range []string{"unread", "garbled"} {
		if row := find(list, name); row.Fits || row.Code != provider.GPUUnavailable || row.Message == "" {
			t.Fatalf("%s: %+v", name, row)
		}
	}
}

// A connection that failed its last check never fits, whatever GPUs it
// reported before.
func TestTargetsNeverFitUnreadyConnections(t *testing.T) {
	box := serverWithTwoL4("gpu-box")
	box.Status, box.Message = connections.StatusFailed, "ssh: handshake failed"
	svc := NewService(Deps{Connections: fakeConnections(box), Bindings: fakeBindings(nil), Authz: allowAll{}})
	list, err := svc.Targets(t.Context(), principal(), GPUTargetQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if row := find(list, "gpu-box"); row.Fits || row.Code != provider.GPUUnavailable || !strings.Contains(row.Message, "ssh: handshake failed") {
		t.Fatalf("failed connection: %+v", row)
	}
}

func TestTargetsFilterByKind(t *testing.T) {
	svc := NewService(Deps{Connections: fakeConnections(serverWithTwoL4("gpu-box"), clusterWithL4Nodes("gke"), digitalOceanAccount("do")),
		Bindings: fakeBindings(nil), Options: fakeOptions("do", h100Size(3.39), l40sSize(1.57)), Authz: allowAll{}})
	list, err := svc.Targets(t.Context(), principal(), GPUTargetQuery{Kind: "nvidia-h100-80gb"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Targets) != 1 || list.Targets[0].SizeID != "gpu-h100x1-80gb" {
		t.Fatalf("rows: %+v", list.Targets)
	}
}

// A server size is offered only when it has enough GPUs with enough memory
// and an image that ships its driver in a region the size is sold in.
func TestTargetsOfferOnlyCreatableSizes(t *testing.T) {
	noImage := h100Size(3.39)
	noImage.ID, noImage.Regions = "gpu-h100x1-ams", []string{"ams3"}
	amd := provider.ServerSize{ID: "gpu-mi300x1-192gb", HourlyUSD: 1.99, HourlyPriceKnown: true, Regions: []string{"tor1"}, Accelerator: &provider.Accelerator{Kind: "amd-mi300x", Vendor: provider.VendorAMD, Count: 1, MemoryGiB: 192}}
	cpu := provider.ServerSize{ID: "s-2vcpu-4gb", HourlyUSD: 0.03, HourlyPriceKnown: true, Regions: []string{"tor1"}}
	eight := h100Size(23.92)
	eight.ID, eight.Accelerator = "gpu-h100x8-640gb", &provider.Accelerator{Kind: "nvidia-h100-80gb", Vendor: provider.VendorNVIDIA, Count: 8, MemoryGiB: 80}
	svc := NewService(Deps{Connections: fakeConnections(digitalOceanAccount("do")), Bindings: fakeBindings(nil),
		Options: fakeOptions("do", h100Size(3.39), noImage, amd, cpu, eight, l40sSize(1.57)), Authz: allowAll{}})
	list, err := svc.Targets(t.Context(), principal(), GPUTargetQuery{Count: 2, MinMemoryGiB: 60})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Targets) != 1 {
		t.Fatalf("rows: %+v", list.Targets)
	}
	row := list.Targets[0]
	if row.SizeID != "gpu-h100x8-640gb" || row.Free != 8 || !row.Fits || row.Cloud != connections.DigitalOcean || len(row.Regions) != 1 || row.Regions[0] != "tor1" {
		t.Fatalf("size: %+v", row)
	}
}

// A server connection is priced from the managed server that runs it, and
// an unknown price stays unknown rather than reading as free.
func TestTargetsPriceServersFromTheirCloudServer(t *testing.T) {
	servers := fakeServers(
		&cloudinfra.Server{ID: "srv-1", RuntimeConnectionID: "priced", Ownership: "managed", Status: "ready", HourlyUSD: 0.76, HourlyPriceKnown: true},
		&cloudinfra.Server{ID: "srv-2", RuntimeConnectionID: "unpriced", Ownership: "managed", Status: "ready", HourlyUSD: 0.5},
	)
	svc := NewService(Deps{Connections: fakeConnections(serverWithTwoL4("priced"), serverWithTwoL4("unpriced"), serverWithTwoL4("adopted")),
		Bindings: fakeBindings(nil), Servers: servers, Authz: allowAll{}})
	list, err := svc.Targets(t.Context(), principal(), GPUTargetQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if row := find(list, "priced"); row.HourlyUSD != 0.76 || !row.HourlyPriceKnown || row.PriceEstimate {
		t.Fatalf("priced: %+v", row)
	}
	for _, name := range []string{"unpriced", "adopted"} {
		if row := find(list, name); row.HourlyUSD != 0 || row.HourlyPriceKnown {
			t.Fatalf("%s: %+v", name, row)
		}
	}
}

// Fitting rows come first, cheapest first with unknown prices last, then by name.
func TestTargetsSortFittingCheapestFirst(t *testing.T) {
	servers := fakeServers(&cloudinfra.Server{ID: "srv", RuntimeConnectionID: "b-box", Ownership: "managed", Status: "ready", HourlyUSD: 0.5, HourlyPriceKnown: true})
	svc := NewService(Deps{Connections: fakeConnections(serverWithTwoL4("a-box"), serverWithTwoL4("b-box"), serverWithToolkitMissing("c-box"), clusterWithL4Nodes("gke"), digitalOceanAccount("do")),
		Bindings: fakeBindings(nil), Servers: servers, Options: fakeOptions("do", h100Size(3.39)), Authz: allowAll{}})
	list, err := svc.Targets(t.Context(), principal(), GPUTargetQuery{})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(list.Targets))
	for _, r := range list.Targets {
		got = append(got, r.Name)
	}
	want := []string{"b-box", "gke", "gpu-h100x1-80gb", "a-box", "c-box"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestTargetsReportBindingErrors(t *testing.T) {
	svc := NewService(Deps{Connections: fakeConnections(serverWithTwoL4("gpu-box")), Bindings: failingBindings{}, Authz: allowAll{}})
	if _, err := svc.Targets(t.Context(), principal(), GPUTargetQuery{}); err == nil {
		t.Fatal("a failed binding read listed GPUs as free")
	}
}

// fakes

func principal() security.Principal { return security.Principal{OrgID: "org", UserID: "usr"} }

type allowAll struct{}

func (allowAll) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

type denyAuthz struct{ denied security.Permission }

func (a denyAuthz) Require(_ context.Context, _ security.Principal, perm security.Permission, _ string) error {
	if perm == a.denied {
		return security.ErrForbidden
	}
	return nil
}

type connectionList struct {
	conns []*connections.Connection
	lists int
}

func (c *connectionList) List(_ context.Context, p security.Principal) ([]*connections.Connection, error) {
	c.lists++
	out := make([]*connections.Connection, 0, len(c.conns))
	for _, v := range c.conns {
		cp := *v
		cp.OrgID = p.OrgID
		out = append(out, &cp)
	}
	return out, nil
}

func fakeConnections(conns ...*connections.Connection) *connectionList {
	return &connectionList{conns: conns}
}

type bindingMap map[string]map[string]int

func (b bindingMap) GPUBindings(context.Context, string) (map[string]map[string]int, error) {
	return b, nil
}

func fakeBindings(m map[string]map[string]int) bindingMap { return bindingMap(m) }

type failingBindings struct{}

func (failingBindings) GPUBindings(context.Context, string) (map[string]map[string]int, error) {
	return nil, errors.New("database is down")
}

type optionsByConnection map[string]provider.ServerOptions

func (o optionsByConnection) AccountServerOptions(_ context.Context, _, id string) (provider.ServerOptions, error) {
	options, ok := o[id]
	if !ok {
		return provider.ServerOptions{}, errors.New("no such account")
	}
	return options, nil
}

// fakeOptions answers for one DigitalOcean account; "" answers for none.
func fakeOptions(id string, sizes ...provider.ServerSize) optionsByConnection {
	if id == "" {
		return optionsByConnection{}
	}
	return optionsByConnection{id: {
		Regions: []provider.ServerRegion{{ID: "tor1", Name: "Toronto 1"}, {ID: "nyc2", Name: "New York 2"}, {ID: "ams3", Name: "Amsterdam 3"}},
		Sizes:   sizes,
		Images: []provider.ServerImage{
			{ID: "gpu-h100x1-base", Name: "AI/ML Ready", Regions: []string{"tor1"}, Bootstrap: true, Accelerators: []string{provider.VendorNVIDIA}},
			{ID: "ubuntu-24-04-x64", Name: "Ubuntu 24.04", Regions: []string{"tor1", "nyc2", "ams3"}, Bootstrap: true},
		},
	}}
}

// failingOptions fails for every account ok doesn't answer for.
type failingOptions struct {
	err error
	ok  optionsByConnection
}

func (f failingOptions) AccountServerOptions(ctx context.Context, org, id string) (provider.ServerOptions, error) {
	if _, answers := f.ok[id]; answers {
		return f.ok.AccountServerOptions(ctx, org, id)
	}
	return provider.ServerOptions{}, f.err
}

type serverList []*cloudinfra.Server

func (s serverList) RuntimeServers(context.Context, string) ([]*cloudinfra.Server, error) {
	return s, nil
}

func fakeServers(servers ...*cloudinfra.Server) serverList { return serverList(servers) }

func l4(index int) provider.GPUDevice {
	return provider.GPUDevice{Index: index, UUID: "GPU-" + string(rune('a'+index)), Kind: "nvidia-l4", Product: "NVIDIA L4", MemoryMiB: 23034}
}

func serverWithTwoL4(id string) *connections.Connection {
	inv := provider.GPUInventory{Status: provider.GPUStatusReady, Devices: []provider.GPUDevice{l4(0), l4(1)}}
	return &connections.Connection{ID: id, Name: id, Cloud: connections.Server, Status: connections.StatusReady, Details: inv.Details()}
}

func serverWithToolkitMissing(id string) *connections.Connection {
	inv := provider.GPUInventory{Status: provider.GPUStatusToolkitMissing, Devices: []provider.GPUDevice{l4(0)},
		Message: "This server has 1 NVIDIA L4 GPU but Docker can't use them. Install the NVIDIA Container Toolkit, run nvidia-ctk runtime configure --runtime=docker, restart Docker, then check the server again."}
	return &connections.Connection{ID: id, Name: id, Cloud: connections.Server, Status: connections.StatusReady, Details: inv.Details()}
}

func clusterWithL4Nodes(id string) *connections.Connection {
	inv := provider.GPUInventory{Status: provider.GPUStatusReady, Nodes: []provider.GPUNodeGroup{{
		Kind: "nvidia-l4", Product: "NVIDIA-L4", MemoryGiB: 24, Nodes: 2, PerNode: 1, Total: 2, InstanceType: "g2-standard-8",
		NodeSelector: map[string]string{"cloud.google.com/gke-accelerator": "nvidia-l4"}, HourlyUSD: 0.85, HourlyPriceKnown: true,
	}}}
	return &connections.Connection{ID: id, Name: id, Cloud: connections.Kubernetes, Status: connections.StatusReady, Details: inv.Details()}
}

func digitalOceanAccount(id string) *connections.Connection {
	return &connections.Connection{ID: id, Name: id, Cloud: connections.DigitalOcean, Status: connections.StatusReady, Details: map[string]string{}}
}

func h100Size(hourly float64) provider.ServerSize {
	return provider.ServerSize{ID: "gpu-h100x1-80gb", CPUs: 20, MemoryMiB: 240 << 10, DiskGB: 720, HourlyUSD: hourly, HourlyPriceKnown: true,
		Regions: []string{"nyc2", "tor1"}, Accelerator: &provider.Accelerator{Kind: "nvidia-h100-80gb", Vendor: provider.VendorNVIDIA, Count: 1, MemoryGiB: 80}}
}

func l40sSize(hourly float64) provider.ServerSize {
	return provider.ServerSize{ID: "gpu-l40sx1-48gb", CPUs: 8, MemoryMiB: 64 << 10, DiskGB: 500, HourlyUSD: hourly, HourlyPriceKnown: true,
		Regions: []string{"tor1"}, Accelerator: &provider.Accelerator{Kind: "nvidia-l40s", Vendor: provider.VendorNVIDIA, Count: 1, MemoryGiB: 48}}
}

func find(list *GPUTargetList, name string) GPUTarget {
	if list == nil {
		return GPUTarget{}
	}
	for _, r := range list.Targets {
		if r.Source == SourceConnection && r.Name == name {
			return r
		}
	}
	return GPUTarget{}
}

func findSize(list *GPUTargetList, size string) GPUTarget {
	if list == nil {
		return GPUTarget{}
	}
	for _, r := range list.Targets {
		if r.Source == SourceServerSize && r.SizeID == size {
			return r
		}
	}
	return GPUTarget{}
}

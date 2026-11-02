package models

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/gpus"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// The server-first fixture: a DigitalOcean account in staging's cloud
// that sells a one-L40S size in tor1.
const (
	doConn     = "conn_do"
	cloudID    = "cloud_1"
	l40sSize   = "gpu-l40sx1-48gb"
	gpuImageID = "gpu-h100x1-base"
	newConn    = "conn_new"
)

// cloudAdmin may create GPU servers.
var cloudAdmin = append(slices.Clone(aiAdmin), security.OrganizationCloudManage)

// byUser is an Authorizer that grants each user their own permissions,
// so a test can take one away from the person who deployed.
type byUser struct {
	mu    sync.Mutex
	users map[string]perms
}

func (b *byUser) of(p security.Principal) perms {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.users[p.UserID]
}

func (b *byUser) set(user string, ps perms) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.users[user] = ps
}

func (b *byUser) Require(ctx context.Context, p security.Principal, perm security.Permission, id string) error {
	return b.of(p).Require(ctx, p, perm, id)
}

func (b *byUser) Allowed(ctx context.Context, p security.Principal, perm security.Permission) (bool, error) {
	return b.of(p).Allowed(ctx, p, perm)
}

// fakeServers is cloudinfra as the models service uses it, in memory. It
// records every call, and checks organization cloud management as the
// real one does.
type fakeServers struct {
	mu        sync.Mutex
	authz     Authorizer
	now       func() time.Time
	options   provider.ServerOptions
	ops       map[string]*cloudinfra.Operation
	servers   []*cloudinfra.Server
	hourly    float64
	priced    bool
	previews  []cloudinfra.ServerInput
	applies   []string
	reads     int
	applyErrs []error // returned by the next applies, in order
	// onApply runs at the start of each apply: what happens elsewhere
	// while it's in flight.
	onApply func()
}

func (f *fakeServers) allowed(ctx context.Context, p security.Principal) error {
	return f.authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID)
}

func (f *fakeServers) ServerOptions(ctx context.Context, p security.Principal, _, _ string) (provider.ServerOptions, error) {
	if err := f.allowed(ctx, p); err != nil {
		return provider.ServerOptions{}, err
	}
	return f.options, nil
}

func (f *fakeServers) PreviewServer(ctx context.Context, p security.Principal, cloud string, in cloudinfra.ServerInput) (*cloudinfra.Operation, error) {
	if err := f.allowed(ctx, p); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.previews = append(f.previews, in)
	op := &cloudinfra.Operation{
		ID: fmt.Sprintf("infraop_%d", len(f.previews)), OrgID: p.OrgID, CloudID: cloud, ServerID: fmt.Sprintf("server_%d", len(f.previews)),
		Review: fmt.Sprintf("review-%d", len(f.previews)), Input: in, Provider: connections.DigitalOcean, ConnectionID: in.ConnectionID,
		Status: "preview", Ownership: "managed", EstimatedHourlyUSD: f.hourly, HourlyPriceKnown: f.priced,
		Accelerator: &provider.Accelerator{Kind: "nvidia-l40s", Vendor: provider.VendorNVIDIA, Count: 1, MemoryGiB: 48},
		CreatedAt:   f.now(), UpdatedAt: f.now(), ExpiresAt: f.now().Add(15 * time.Minute), CapacityCheckedAt: f.now(),
	}
	if f.ops == nil {
		f.ops = map[string]*cloudinfra.Operation{}
	}
	f.ops[op.ID] = op
	out := *op
	return &out, nil
}

func (f *fakeServers) ApplyServerOperation(ctx context.Context, p security.Principal, cloud, id string, in cloudinfra.ApplyInput) (*cloudinfra.Operation, error) {
	if err := f.allowed(ctx, p); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applies = append(f.applies, id)
	if f.onApply != nil {
		f.onApply()
	}
	op, ok := f.ops[id]
	if !ok || op.CloudID != cloud {
		return nil, cloudinfra.ErrNotFound
	}
	if op.Review != in.Review {
		return nil, cloudinfra.ErrConflict
	}
	if len(f.applyErrs) > 0 {
		err := f.applyErrs[0]
		f.applyErrs = f.applyErrs[1:]
		var late afterAcceptError
		if errors.As(err, &late) {
			// The accept committed, then the caller got an error.
			op.Status, op.UpdatedAt = "pending", f.now()
			return nil, late.err
		}
		if err != nil {
			return nil, err
		}
	}
	if op.Status == "preview" {
		op.Status, op.UpdatedAt = "pending", f.now()
	}
	out := *op
	return &out, nil
}

// afterAcceptError is an apply error that comes after the accept committed: a
// lost race with another apply, or a failure on the way back.
type afterAcceptError struct{ err error }

func (a afterAcceptError) Error() string { return a.err.Error() }

func (f *fakeServers) GetOperation(ctx context.Context, p security.Principal, cloud, id string) (*cloudinfra.Operation, error) {
	if err := f.allowed(ctx, p); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	op, ok := f.ops[id]
	if !ok || op.CloudID != cloud || op.OrgID != p.OrgID {
		return nil, cloudinfra.ErrNotFound
	}
	out := *op
	return &out, nil
}

func (f *fakeServers) ListServers(ctx context.Context, p security.Principal, cloud string) ([]*cloudinfra.Server, error) {
	if err := f.allowed(ctx, p); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*cloudinfra.Server
	for _, v := range f.servers {
		if v.CloudID == cloud {
			c := *v
			out = append(out, &c)
		}
	}
	return out, nil
}

// OpenServerCreates is PgStore.OpenCreates over the fake's operations.
func (f *fakeServers) OpenServerCreates(ctx context.Context, p security.Principal, cloud string) ([]*cloudinfra.Operation, error) {
	if err := f.allowed(ctx, p); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*cloudinfra.Operation
	for _, id := range slices.Sorted(maps.Keys(f.ops)) {
		op := f.ops[id]
		if op.CloudID == cloud && op.OrgID == p.OrgID && op.Input.Kind == "create" && !slices.Contains([]string{"preview", "succeeded", "cancelled"}, op.Status) {
			c := *op
			out = append(out, &c)
		}
	}
	return out, nil
}

// succeed marks operation id done at the fake's now, with its server up on
// newConn.
func (f *fakeServers) succeed(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	op := f.ops[id]
	op.Status, op.Enrolled, op.UpdatedAt = "succeeded", true, f.now()
	v := &cloudinfra.Server{
		ID: op.ServerID, OrgID: op.OrgID, CloudID: op.CloudID, Name: op.Input.Name, Ownership: "managed", Provider: connections.DigitalOcean,
		ConnectionID: op.ConnectionID, RuntimeConnectionID: newConn, Region: op.Input.Region, Size: op.Input.Size, Status: "ready",
		HourlyUSD: op.EstimatedHourlyUSD, HourlyPriceKnown: op.HourlyPriceKnown,
	}
	f.servers = append(f.servers, v)
}

func (f *fakeServers) set(id string, change func(*cloudinfra.Operation)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f.ops[id])
}

func (f *fakeServers) calls() (previews, applies, reads int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.previews), len(f.applies), f.reads
}

// fakeClouds assigns every environment of the project to one cloud.
type fakeClouds struct {
	assignment *cloudprofiles.Assignment
}

func (f fakeClouds) EnvironmentCloud(_ context.Context, _ security.Principal, _, _ string) (*cloudprofiles.Assignment, int64, error) {
	if f.assignment == nil {
		return nil, 0, nil
	}
	a := *f.assignment
	return &a, a.Revision, nil
}

// Waiting and Stranded make memDeployments the cron's WaitingLister.
func (m *memDeployments) Waiting(context.Context) ([]*Deployment, error) {
	return m.list(func(d *Deployment) bool { return d.Phase == PhaseWaitingForServer }), nil
}

func (m *memDeployments) Stranded(_ context.Context, before time.Time) ([]*Deployment, error) {
	return m.list(func(d *Deployment) bool {
		return d.Phase == PhasePlanned && d.PlanID == "" && d.Message == "" && d.RequestedBy != (security.Principal{}) && d.UpdatedAt.Before(before)
	}), nil
}

// l40sSizeRow is the size DigitalOcean sells: one L40S, $1.57 an hour.
func l40sSizeRow() gpus.GPUTarget {
	return gpus.GPUTarget{
		Source: gpus.SourceServerSize, ConnectionID: doConn, Name: l40sSize, Cloud: connections.DigitalOcean, Regions: []string{"tor1"},
		SizeID: l40sSize, Accelerator: provider.Accelerator{Kind: "nvidia-l40s", Vendor: provider.VendorNVIDIA, Count: 1, MemoryGiB: 48},
		Free: 1, Fits: true, HourlyUSD: 1.57, HourlyPriceKnown: true,
	}
}

type serverFirstSetup struct {
	*deploySetup
	servers *fakeServers
	clouds  *fakeClouds
	conns   *[]*connections.Connection
}

// newServerFirst is a deploy setup with the DigitalOcean account in
// staging's and production's cloud, as authz.
func newServerFirst(t *testing.T, authz Authorizer) *serverFirstSetup {
	t.Helper()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), authz, []gpus.GPUTarget{l40sSizeRow()})
	d.rows.now = d.svc.d.Now
	conns := []*connections.Connection{}
	sf := &serverFirstSetup{
		deploySetup: d,
		servers: &fakeServers{authz: authz, now: d.svc.d.Now, hourly: 1.57, priced: true, options: provider.ServerOptions{
			Regions: []provider.ServerRegion{{ID: "tor1"}},
			Sizes:   []provider.ServerSize{{ID: l40sSize, DiskGB: 500, Regions: []string{"tor1"}}},
			Images: []provider.ServerImage{
				{ID: "ubuntu-24-04-x64", Regions: []string{"tor1"}, Bootstrap: true},
				{ID: "gpu-amd-base", Regions: []string{"tor1"}, Bootstrap: true, Accelerators: []string{provider.VendorAMD}},
				{ID: gpuImageID, Regions: []string{"tor1"}, Bootstrap: true, Accelerators: []string{provider.VendorNVIDIA}},
			},
		}},
		clouds: &fakeClouds{assignment: &cloudprofiles.Assignment{
			OrgID: "org", ProjectID: projectID, CloudID: cloudID, CloudRevision: 7,
			Targets: []cloudprofiles.Target{{ID: "tgt_do", CloudID: cloudID, Provider: connections.DigitalOcean, ConnectionID: doConn, Region: "tor1"}},
		}},
		conns: &conns,
	}
	d.svc.d.Servers, d.svc.d.Clouds, d.svc.d.Waiting = sf.servers, sf.clouds, d.rows
	d.svc.d.Connections = connList{authz: authz, list: sf.conns}
	return sf
}

// connList is fakeConnections over a list a test can add to.
type connList struct {
	authz Authorizer
	list  *[]*connections.Connection
}

func (c connList) List(ctx context.Context, p security.Principal) ([]*connections.Connection, error) {
	return fakeConnections{authz: c.authz, list: *c.list}.List(ctx, p)
}

func onSize() TargetChoice {
	return TargetChoice{Source: TargetServerSize, ConnectionID: doConn, Region: "tor1", SizeID: l40sSize, GPU: &provider.GPURequest{Kind: "nvidia-l40s"}}
}

// serverName is the server a deploy of Qwen 7B to staging in the shop
// project creates.
const serverName = "shop-qwen2-5-7b-instruct-staging-gpu"

// deployOnNewServer previews Qwen 7B on a new L40S server in staging and
// deploys it with the review approved.
func (sf *serverFirstSetup) deployOnNewServer(t *testing.T) (*DeployPreview, *DeployResult) {
	t.Helper()
	ctx := context.Background()
	in := DeployInput{EntryID: qwen7B, Target: onSize(), ReviewServer: true}
	pv, err := sf.svc.Preview(ctx, member, projectID, stagingID, in)
	if err != nil || len(pv.Problems) != 0 || pv.Server == nil {
		t.Fatalf("preview: %+v, %v", pv, err)
	}
	in.ReviewServer = false
	in.Server = &ServerApproval{CloudID: pv.Server.CloudID, OperationID: pv.Server.ID, Review: pv.Server.Review}
	res, err := sf.svc.Deploy(ctx, member, projectID, stagingID, in)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	return pv, res
}

// gpusReady puts the new server's runtime connection in the workspace,
// with its GPU inventory status, and as a GPU target once it's ready.
func (sf *serverFirstSetup) gpusReady(status string) {
	inv := provider.GPUInventory{Status: status}
	if status == provider.GPUStatusReady {
		inv.Devices = []provider.GPUDevice{{Kind: "nvidia-l40s", MemoryMiB: 49152}}
	}
	*sf.conns = []*connections.Connection{{ID: newConn, Name: serverName, Cloud: connections.Server, Status: connections.StatusReady, Details: inv.Details()}}
	sf.targets.targets = []gpus.GPUTarget{l40sSizeRow()}
	if status == provider.GPUStatusReady {
		row := l40sServer()
		row.ConnectionID, row.Name = newConn, serverName
		sf.targets.targets = append(sf.targets.targets, row)
	}
}

func (sf *serverFirstSetup) row(t *testing.T) *Deployment {
	t.Helper()
	d, err := sf.rows.Get(context.Background(), "org", projectID, stagingID, "qwen2-5-7b-instruct")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (sf *serverFirstSetup) advance(d time.Duration) { *sf.now = sf.now.Add(d) }

func TestPreviewCreatesAServerReview(t *testing.T) {
	sf := newServerFirst(t, cloudAdmin)
	sf.servers.hourly = 1.99 // the provider's price now, not the cached list

	// A plain preview prices the size and reviews nothing (Ruling M28).
	pv, err := sf.svc.Preview(context.Background(), member, projectID, stagingID, DeployInput{EntryID: qwen7B, Target: onSize()})
	if err != nil || len(pv.Problems) != 0 || pv.CanDeploy || !pv.ServerReviewNeeded || pv.Server != nil || pv.TargetName != serverName {
		t.Fatalf("plain preview: problems %v can deploy %v review needed %v, %v", pv.Problems, pv.CanDeploy, pv.ServerReviewNeeded, err)
	}
	if pv.Cost.HourlyUSD != "1.5700" || pv.Cost.Source != CostSourceServer || len(sf.servers.previews) != 0 {
		t.Fatalf("plain preview cost %+v, %d reviews", pv.Cost, len(sf.servers.previews))
	}

	// The review step asks the provider and records the review.
	pv, err = sf.svc.Preview(context.Background(), member, projectID, stagingID, DeployInput{EntryID: qwen7B, Target: onSize(), ReviewServer: true})
	if err != nil || len(pv.Problems) != 0 || !pv.CanDeploy || pv.ServerReviewNeeded {
		t.Fatalf("review: problems %v can deploy %v, %v", pv.Problems, pv.CanDeploy, err)
	}
	if len(sf.servers.previews) != 1 {
		t.Fatalf("previews %+v", sf.servers.previews)
	}
	want := cloudinfra.ServerInput{Kind: "create", ConnectionID: doConn, Name: serverName, Region: "tor1", Size: l40sSize, Image: gpuImageID, CloudRevision: 7}
	if got := sf.servers.previews[0]; got != want {
		t.Errorf("server input %+v", got)
	}
	if pv.Server == nil || pv.Server.CloudID != cloudID || pv.TargetName != serverName || pv.Target.Provider != connections.Server {
		t.Errorf("server %+v target name %q provider %q", pv.Server, pv.TargetName, pv.Target.Provider)
	}
	if pv.Cost.Source != CostSourceServer || pv.Cost.HourlyUSD != "1.9900" || pv.Cost.Share != "1 of 1 GPUs" || pv.Cost.MonthlyUSD != "1452.70" {
		t.Errorf("cost %+v", pv.Cost)
	}
	if pv.GPU == nil || *pv.GPU != (provider.GPURequest{Kind: "nvidia-l40s", Count: 1}) {
		t.Errorf("gpu %+v", pv.GPU)
	}
	// The preview wrote no deployment and no manifest.
	if sf.revisions.count() != 1 || len(sf.rows.rows) != 0 || len(sf.servers.applies) != 0 {
		t.Error("Preview deployed something")
	}

	// A price the provider doesn't know is unknown, never zero.
	sf.servers.priced, sf.servers.hourly = false, 0
	pv, err = sf.svc.Preview(context.Background(), member, projectID, stagingID, DeployInput{EntryID: qwen7B, Target: onSize(), ReviewServer: true})
	if err != nil || pv.Cost.Source != CostSourceUnknown || pv.Cost.HourlyUSD != "" || pv.Cost.Known {
		t.Errorf("unknown price: %+v, %v", pv.Cost, err)
	}
}

func TestServerFirstNeedsAnAdmin(t *testing.T) {
	sf := newServerFirst(t, deployer)
	pv, err := sf.svc.Preview(context.Background(), member, projectID, stagingID, DeployInput{EntryID: qwen7B, Target: onSize(), ReviewServer: true})
	if err != nil || !slices.Equal(codes(pv), []string{ProblemServerAdmin}) || pv.CanDeploy ||
		pv.Problems[0].Message != "An admin creates GPU servers. Ask one to deploy this, or pick a server you have." {
		t.Fatalf("developer: %+v, %v", pv, err)
	}
	if len(sf.servers.previews) != 0 {
		t.Error("a developer's preview reviewed a server")
	}
	// The size's list price still shows, as the whole server's.
	if pv.Cost.HourlyUSD != "1.5700" || pv.Cost.Source != CostSourceServer {
		t.Errorf("cost %+v", pv.Cost)
	}
}

func TestServerFirstNeedsTheAccountInTheCloud(t *testing.T) {
	sf := newServerFirst(t, cloudAdmin)
	sf.clouds.assignment.Targets = []cloudprofiles.Target{{ID: "tgt_k", Provider: connections.Kubernetes, ConnectionID: kubeConn}}
	pv, err := sf.svc.Preview(context.Background(), member, projectID, stagingID, DeployInput{EntryID: qwen7B, Target: onSize()})
	if err != nil || !slices.Equal(codes(pv), []string{ProblemNoCloud}) || pv.Problems[0].Message != "Add this DigitalOcean account to staging's cloud first." {
		t.Errorf("account outside the cloud: %+v, %v", pv.Problems, err)
	}
	sf.clouds.assignment = nil
	if pv, err = sf.svc.Preview(context.Background(), member, projectID, stagingID, DeployInput{EntryID: qwen7B, Target: onSize()}); err != nil ||
		!slices.Equal(codes(pv), []string{ProblemNoCloud}) {
		t.Errorf("no cloud: %+v, %v", pv.Problems, err)
	}
	if len(sf.servers.previews) != 0 {
		t.Error("a server was reviewed outside the cloud")
	}
}

func TestDeployAppliesTheServerAndWaits(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)

	// Without the approval, or with another review, nothing happens.
	in := DeployInput{EntryID: qwen7B, Target: onSize()}
	var refused *ProblemError
	if _, err := sf.svc.Deploy(ctx, member, projectID, stagingID, in); !errors.As(err, &refused) || refused.Problem.Code != ProblemServerReview {
		t.Errorf("no approval: %v", err)
	}
	in.ReviewServer = true
	pv, err := sf.svc.Preview(ctx, member, projectID, stagingID, in)
	if err != nil {
		t.Fatal(err)
	}
	in.ReviewServer = false
	in.Server = &ServerApproval{CloudID: cloudID, OperationID: pv.Server.ID, Review: "not-the-review"}
	if _, err = sf.svc.Deploy(ctx, member, projectID, stagingID, in); !errors.As(err, &refused) || refused.Problem.Code != ProblemServerReview {
		t.Errorf("wrong review: %v", err)
	}
	if len(sf.servers.applies) != 0 || len(sf.rows.rows) != 0 {
		t.Fatal("a refused deploy approved a server or wrote a row")
	}

	in.Server.Review = pv.Server.Review
	res, err := sf.svc.Deploy(ctx, member, projectID, stagingID, in)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sf.servers.applies, []string{pv.Server.ID}) || res.PlanID != "" {
		t.Errorf("applies %v plan %q", sf.servers.applies, res.PlanID)
	}
	d := sf.row(t)
	if d.Phase != PhaseWaitingForServer || d.ServerCloudID != cloudID || d.ServerOperationID != pv.Server.ID || d.ServerName != serverName ||
		d.Message != "" || d.RequestedBy != member || d.Request == nil || d.Request.Target.SizeID != l40sSize || d.Request.Quantization != "bf16" ||
		d.Request.Server == nil || d.Request.Server.Review != pv.Server.Review {
		t.Errorf("row %+v request %+v", d, d.Request)
	}
	// No manifest edit and no plan until the server is up.
	if sf.revisions.count() != 1 || len(sf.plans.asked) != 0 {
		t.Error("the manifest or a plan changed before the server")
	}
	if len(sf.audit.events) != 1 || sf.audit.events[0].Action != "ai.model.server" {
		t.Errorf("audit %+v", sf.audit.events)
	}
	// A used review can't deploy again.
	if _, err = sf.svc.Deploy(ctx, member, projectID, productID, in); !errors.As(err, &refused) || refused.Problem.Code != ProblemServerReview {
		t.Errorf("used review: %v", err)
	}
}

func staleCount() error {
	return &cloudinfra.CapacityAdmissionError{Block: cloudinfra.CapacityBlock{Reason: "stale_inventory", Resource: "inventory", CloudID: cloudID, Retryable: true}}
}

func gpuCeiling() error {
	return &cloudinfra.CapacityAdmissionError{Block: cloudinfra.CapacityBlock{Reason: "hard_limit", Resource: "gpus:nvidia-l40s", CloudID: cloudID, Limit: 2, Counted: 2, Requested: 1}}
}

// The capacity policy holds the server back for now: the deploy waits,
// saying why, and the cron approves the same review again until the cloud
// has room.
func TestCapacityPauseKeepsWaiting(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	sf.servers.applyErrs = []error{staleCount(), staleCount()}
	pv, _ := sf.deployOnNewServer(t)
	want := "The cloud's capacity count of servers and their resources is out of date, so the GPU server waits until Loomplane has counted again."
	if d := sf.row(t); d.Phase != PhaseWaitingForServer || d.Message != want {
		t.Fatalf("paused deploy: %s %q", d.Phase, d.Message)
	}
	sf.advance(time.Minute)
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if d := sf.row(t); d.Phase != PhaseWaitingForServer || d.Message != want {
		t.Fatalf("still paused: %s %q", d.Phase, d.Message)
	}
	sf.advance(time.Minute)
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if d := sf.row(t); d.Phase != PhaseWaitingForServer || d.Message != "" {
		t.Errorf("room again: %s %q", d.Phase, d.Message)
	}
	// The same review each time, and never a new server.
	if !slices.Equal(sf.servers.applies, []string{pv.Server.ID, pv.Server.ID, pv.Server.ID}) || len(sf.servers.previews) != 1 {
		t.Errorf("applies %v previews %d", sf.servers.applies, len(sf.servers.previews))
	}
}

// A refusal, here a GPU ceiling, refuses the deploy with the reason; one
// the cron meets needs attention.
func TestCapacityRefusal(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	sf.servers.applyErrs = []error{gpuCeiling()}
	in := DeployInput{EntryID: qwen7B, Target: onSize(), ReviewServer: true}
	pv, err := sf.svc.Preview(ctx, member, projectID, stagingID, in)
	if err != nil {
		t.Fatal(err)
	}
	in.ReviewServer = false
	in.Server = &ServerApproval{CloudID: cloudID, OperationID: pv.Server.ID, Review: pv.Server.Review}
	_, err = sf.svc.Deploy(ctx, member, projectID, stagingID, in)
	want := "The GPU server would take the cloud past its capacity limit of 2 NVIDIA L40S GPUs: 2 are counted and it needs 1 more. An admin can raise the limit, then deploy the model again."
	var refused *ProblemError
	if !errors.As(err, &refused) || refused.Problem.Code != ProblemCapacity || err.Error() != want {
		t.Fatalf("refused deploy: %v", err)
	}
	if len(sf.rows.rows) != 0 {
		t.Error("a refused server left its row")
	}

	sf.servers.applyErrs = []error{staleCount(), gpuCeiling()}
	sf.deployOnNewServer(t)
	if err = sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if d := sf.row(t); d.Phase != PhaseNeedsAttention || d.Message != want {
		t.Errorf("refused later: %s %q", d.Phase, d.Message)
	}
	// The review was never applied, so Retry has nothing to carry on: it
	// says to review a new server, and the row keeps its reason.
	_, err = sf.svc.Retry(ctx, member, projectID, stagingID, "qwen2-5-7b-instruct")
	if !errors.Is(err, ErrInvalid) || !strings.HasSuffix(err.Error(),
		": "+serverName+" was never created, and Retry can't approve its review again. Cancel this and deploy the model again to review a new server") {
		t.Errorf("retry after a refusal: %v", err)
	}
	if d := sf.row(t); d.Phase != PhaseNeedsAttention || d.Message != want {
		t.Errorf("after the refused retry: %s %q", d.Phase, d.Message)
	}
}

func TestReconcilePlansOnceTheGPUIsReady(t *testing.T) {
	ctx := context.Background()
	authz := &byUser{users: map[string]perms{"usr_1": cloudAdmin, "usr_2": cloudAdmin}}
	sf := newServerFirst(t, authz)
	pv, _ := sf.deployOnNewServer(t)

	// Still being created: nothing changes.
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if d := sf.row(t); d.Phase != PhaseWaitingForServer || sf.revisions.count() != 1 {
		t.Fatalf("while creating: %s", d.Phase)
	}

	sf.servers.succeed(pv.Server.ID)
	sf.gpusReady(provider.GPUStatusReady)
	sf.advance(2 * time.Minute)
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	d := sf.row(t)
	if d.Phase != PhasePlanned || d.PlanID == "" || d.Message != "" || d.ServerName != serverName {
		t.Fatalf("row %+v", d)
	}
	res := current(t, sf.deploySetup).Resources["qwen2-5-7b-instruct"]
	if res.Target != newConn || res.Provider != connections.Server || res.GPU == nil || res.GPU.Kind != "nvidia-l40s" || !enabledIn(res, "staging") {
		t.Errorf("resource %+v", res)
	}
	if len(sf.plans.asked) != 1 || sf.plans.asked[0].Environment != "staging" {
		t.Errorf("plans %+v", sf.plans.asked)
	}
	// The edit was made as the person who deployed.
	i := slices.IndexFunc(sf.audit.events, func(e projects.AuditEvent) bool { return e.Action == "ai.model.deploy" })
	if i < 0 || sf.audit.events[i].Principal != member {
		t.Errorf("audit %+v", sf.audit.events)
	}
	// Nothing more to do.
	if err := sf.svc.Reconcile(ctx); err != nil || len(sf.plans.asked) != 1 {
		t.Errorf("again: %d plans, %v", len(sf.plans.asked), err)
	}
}

func TestReconcileWaitsForTheToolkit(t *testing.T) {
	ctx := context.Background()
	for _, priced := range []bool{true, false} {
		sf := newServerFirst(t, cloudAdmin)
		sf.servers.priced = priced
		if !priced {
			sf.servers.hourly = 0
		}
		pv, _ := sf.deployOnNewServer(t)
		sf.servers.succeed(pv.Server.ID)
		sf.gpusReady(provider.GPUStatusToolkitMissing)

		sf.advance(5 * time.Minute)
		if err := sf.svc.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		if d := sf.row(t); d.Phase != PhaseWaitingForServer || !strings.Contains(d.Message, "Waiting for its GPUs") {
			t.Fatalf("at +5 min: %s %q", d.Phase, d.Message)
		}
		sf.advance(16 * time.Minute)
		if err := sf.svc.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		d := sf.row(t)
		toolkit := "This target has GPUs but Docker can't use them. Install the NVIDIA Container Toolkit, then check the connection again"
		price := "It costs $1.57/hr while it runs."
		if !priced {
			price = "Its price is unknown."
		}
		want := serverName + " can't take GPU work: " + toolkit + ". " + price + " Delete it from Infrastructure or fix it and press Retry."
		if d.Phase != PhaseNeedsAttention || d.Message != want {
			t.Errorf("priced %v at +21 min: %s %q", priced, d.Phase, d.Message)
		}
		if !priced && (strings.Contains(d.Message, "$") || strings.Contains(d.Message, "0.00")) {
			t.Errorf("an unknown price reads as money: %q", d.Message)
		}
		if sf.revisions.count() != 1 || len(sf.plans.asked) != 0 {
			t.Error("a model was planned onto a server whose GPUs aren't ready")
		}
	}
}

func TestReconcileStopsOnAFailedServer(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	pv, _ := sf.deployOnNewServer(t)
	sf.servers.set(pv.Server.ID, func(o *cloudinfra.Operation) {
		o.Status, o.Message = "failed", "Infrastructure verification failed. Check the connection, provider scope and pinned server runtime before retrying."
	})
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	d := sf.row(t)
	if d.Phase != PhaseNeedsAttention || !strings.HasPrefix(d.Message, "Creating "+serverName+" failed: Infrastructure verification failed.") {
		t.Errorf("failed server: %s %q", d.Phase, d.Message)
	}

	// Retry puts it back to waiting; the cron finds it failed again.
	if _, err := sf.svc.Retry(ctx, member, projectID, stagingID, d.Resource); err != nil {
		t.Fatal(err)
	}
	if d = sf.row(t); d.Phase != PhaseWaitingForServer || d.Message != "" {
		t.Errorf("after retry: %s %q", d.Phase, d.Message)
	}
	if _, err := sf.svc.Retry(ctx, member, projectID, stagingID, d.Resource); !errors.Is(err, ErrInvalid) {
		t.Errorf("retrying a waiting row: %v", err)
	}
}

func TestReconcileTimesOutAServerThatNeverStarts(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	pv, _ := sf.deployOnNewServer(t)
	sf.advance(44 * time.Minute)
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if d := sf.row(t); d.Phase != PhaseWaitingForServer {
		t.Fatalf("at 44 min: %s", d.Phase)
	}
	sf.advance(2 * time.Minute)
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	d := sf.row(t)
	if d.Phase != PhaseNeedsAttention || d.Message != tooLong || !strings.Contains(d.Message, "Check it in Infrastructure") {
		t.Fatalf("at 46 min: %s %q", d.Phase, d.Message)
	}

	// Retried in Infrastructure, the operation has a new attempt with its
	// own enrollment deadline: a Retry here waits for it instead of
	// bouncing straight back.
	sf.servers.set(pv.Server.ID, func(o *cloudinfra.Operation) {
		o.Status, o.Attempt, o.UpdatedAt, o.BootstrapExpiresAt = "running", 2, *sf.now, sf.now.Add(45*time.Minute)
	})
	if _, err := sf.svc.Retry(ctx, member, projectID, stagingID, d.Resource); err != nil {
		t.Fatal(err)
	}
	sf.advance(30 * time.Minute)
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if d = sf.row(t); d.Phase != PhaseWaitingForServer {
		t.Errorf("after retry, still within the attempt: %s %q", d.Phase, d.Message)
	}
	sf.advance(21 * time.Minute)
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if d = sf.row(t); d.Phase != PhaseNeedsAttention {
		t.Errorf("past the attempt's deadline: %s", d.Phase)
	}
}

func TestReconcileRechecksThePrincipal(t *testing.T) {
	ctx := context.Background()
	authz := &byUser{users: map[string]perms{"usr_1": cloudAdmin}}
	sf := newServerFirst(t, authz)
	pv, _ := sf.deployOnNewServer(t)
	sf.servers.succeed(pv.Server.ID)
	sf.gpusReady(provider.GPUStatusReady)

	// The person who deployed lost ai:edit.
	authz.set("usr_1", perms{security.AIRead, security.InfrastructureRead, security.CloudAccountRead, security.ProjectRead,
		security.ProjectUpdate, security.OrganizationCloudManage})
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	d := sf.row(t)
	if d.Phase != PhaseNeedsAttention || d.Message != "The person who asked for this can't deploy models any more." {
		t.Errorf("row %s %q", d.Phase, d.Message)
	}
	if sf.revisions.count() != 1 || len(sf.plans.asked) != 0 {
		t.Error("planned as someone who can't deploy models")
	}

	// A revoked API key behind the stored principal stops it too.
	authz.set("usr_1", cloudAdmin)
	key := member
	key.KeyID = "key_1"
	d.Phase, d.Message, d.RequestedBy = PhaseWaitingForServer, "", key
	if err := sf.rows.Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	sf.svc.d.Principals = rejectKeys{}
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if d = sf.row(t); d.Phase != PhaseNeedsAttention || len(sf.plans.asked) != 0 {
		t.Errorf("revoked key: %s %q", d.Phase, d.Message)
	}
}

// rejectKeys rejects every saved API key.
type rejectKeys struct{}

func (rejectKeys) ValidatePrincipal(context.Context, security.Principal) error {
	return fmt.Errorf("checked: %w", apikeys.ErrRejected)
}

func TestCancelKeepsTheServer(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	pv, _ := sf.deployOnNewServer(t)
	sf.servers.succeed(pv.Server.ID)
	previews, applies, _ := sf.servers.calls()
	res, err := sf.svc.CancelWaiting(ctx, member, projectID, stagingID, "qwen2-5-7b-instruct")
	if err != nil {
		t.Fatal(err)
	}
	// The server is up, so the result says where to delete it.
	if want := serverName + " was already approved and is up. Delete it in Infrastructure if you don't want it: it costs money while it runs."; res.Message != want {
		t.Errorf("cancel message %q", res.Message)
	}
	if _, err := sf.rows.Get(ctx, "org", projectID, stagingID, "qwen2-5-7b-instruct"); !errors.Is(err, ErrNotFound) {
		t.Errorf("row after cancel: %v", err)
	}
	// The server stays: no new call reached cloudinfra, and it's still there.
	p, a, _ := sf.servers.calls()
	if p != previews || a != applies || len(sf.servers.servers) != 1 || sf.servers.servers[0].Status != "ready" {
		t.Errorf("cloudinfra calls %d/%d, servers %+v", p, a, sf.servers.servers)
	}
	if !slices.ContainsFunc(sf.audit.events, func(e projects.AuditEvent) bool { return e.Action == "ai.model.cancel" }) {
		t.Errorf("audit %+v", sf.audit.events)
	}
	// Nothing left to cancel, and production's models need ai:manage.
	if _, err := sf.svc.CancelWaiting(ctx, member, projectID, stagingID, "qwen2-5-7b-instruct"); !errors.Is(err, ErrNotFound) {
		t.Errorf("cancel again: %v", err)
	}
	dev := newServerFirst(t, deployer)
	if _, err := dev.svc.CancelWaiting(ctx, member, projectID, productID, "x"); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("developer in production: %v", err)
	}
}

// Creating a server costs the user money, so only a deploy someone made
// does it. The cron never creates or approves a server for a row that
// didn't ask for one.
func TestReconcileNeverCreatesAServerUnasked(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	base := func(resource string) *Deployment {
		return &Deployment{OrgID: "org", ProjectID: projectID, EnvironmentID: stagingID, Resource: resource, EntryID: qwen7B,
			Quantization: "bf16", Engine: "vllm", Phase: PhaseWaitingForServer, RequestedBy: member}
	}
	noRequest := base("no-request")
	onAServer := base("on-a-server")
	onAServer.Request = &DeployInput{EntryID: qwen7B, Target: onGPU("nvidia-l40s")}
	unapproved := base("unapproved")
	unapproved.Request = &DeployInput{EntryID: qwen7B, Target: onSize()}
	unapproved.ServerCloudID, unapproved.ServerOperationID = cloudID, "infraop_x"
	otherOrg := base("other-org")
	otherOrg.Request = &DeployInput{EntryID: qwen7B, Target: onSize(), Server: &ServerApproval{CloudID: cloudID, OperationID: "infraop_y", Review: "r"}}
	otherOrg.ServerCloudID, otherOrg.ServerOperationID = cloudID, "infraop_y"
	otherOrg.RequestedBy = security.Principal{UserID: "usr_9", OrgID: "org_other"}
	for _, d := range []*Deployment{noRequest, onAServer, unapproved, otherOrg} {
		if err := sf.rows.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if p, a, r := sf.servers.calls(); p != 0 || a != 0 || r != 0 {
		t.Errorf("the cron reached cloudinfra: %d previews, %d applies, %d reads", p, a, r)
	}
	for _, name := range []string{"no-request", "on-a-server", "unapproved", "other-org"} {
		d, err := sf.rows.Get(ctx, "org", projectID, stagingID, name)
		if err != nil || d.Phase != PhaseNeedsAttention || !strings.HasPrefix(d.Message, "Nobody asked Loomplane to create a GPU server") {
			t.Errorf("%s: %+v, %v", name, d, err)
		}
	}
}

// Rulings M24 and M28: a waiting row whose environment is gone goes; a
// server being created for it is logged and stays in Infrastructure.
func TestReconcileHandlesADeletedEnvironment(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	withServer := &Deployment{OrgID: "org", ProjectID: projectID, EnvironmentID: "env_gone", Resource: "with-server", EntryID: qwen7B,
		Quantization: "bf16", Engine: "vllm", Phase: PhaseWaitingForServer, ServerCloudID: cloudID, ServerOperationID: "infraop_1",
		ServerName: "shop-with-server-gpu", RequestedBy: member}
	without := &Deployment{OrgID: "org", ProjectID: "prj_gone", EnvironmentID: "env_x", Resource: "without", EntryID: qwen7B,
		Quantization: "bf16", Engine: "vllm", Phase: PhaseWaitingForServer, RequestedBy: member}
	for _, d := range []*Deployment{withServer, without} {
		if err := sf.rows.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	var warned []string
	sf.svc.d.Warn = func(msg string, err error) { warned = append(warned, msg+": "+err.Error()) }
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	// Nothing could show or cancel either row, so both go, and the server
	// that stays in Infrastructure is logged.
	if _, err := sf.rows.Get(ctx, "org", projectID, "env_gone", "with-server"); !errors.Is(err, ErrNotFound) {
		t.Errorf("with a server: %v", err)
	}
	if _, err := sf.rows.Get(ctx, "org", "prj_gone", "env_x", "without"); !errors.Is(err, ErrNotFound) {
		t.Errorf("without a server: %v", err)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "shop-with-server-gpu") || !strings.Contains(warned[0], "infraop_1") {
		t.Errorf("warnings %v", warned)
	}
	if p, a, r := sf.servers.calls(); p+a+r != 0 {
		t.Error("a deleted environment's row reached cloudinfra")
	}
}

// Ruling M27: a planned row whose deploy stopped before the manifest edit
// goes once it's StrandedAfter old; one whose edit landed says how to plan
// it.
func TestReconcileTidiesStrandedRows(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	if _, _, err := manifests.NewService(sf.revisions, memProjects{}, aiAdmin, nil).Edit(ctx, member, projectID, manifests.EditInput{
		AddResource: &manifests.NewResource{Name: "edited", Kind: "model-tei", Provider: "server", Target: cpuConn, Version: "1.9", EnabledIn: "staging",
			Settings: map[string]string{"entry": bgeSmall}},
	}); err != nil {
		t.Fatal(err)
	}
	add := func(env, resource string) {
		t.Helper()
		d := &Deployment{OrgID: "org", ProjectID: projectID, EnvironmentID: env, Resource: resource, EntryID: bgeSmall,
			Quantization: "fp32", Engine: "tei", Phase: PhasePlanned, RequestedBy: member}
		if err := sf.rows.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	add(stagingID, "never-edited")
	add(stagingID, "edited")
	add("env_gone", "orphan")

	// Young rows are a deploy still in flight.
	sf.advance(StrandedAfter - time.Minute)
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sf.rows.rows) != 3 {
		t.Fatalf("%d rows before %s", len(sf.rows.rows), StrandedAfter)
	}
	sf.advance(2 * time.Minute)
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	left, _ := sf.rows.List(ctx, "org")
	if len(left) != 1 || left[0].Resource != "edited" || left[0].Message != "The manifest has edited now. Plan staging from the project to deploy it." {
		t.Errorf("left %+v", left)
	}
}

func TestCapacitySentences(t *testing.T) {
	for _, c := range []struct {
		block  cloudinfra.CapacityBlock
		paused bool
		want   string
	}{
		{cloudinfra.CapacityBlock{Reason: "stale_inventory", Resource: "gpus:nvidia-l40s", Retryable: true}, true,
			"The cloud's capacity count of NVIDIA L40S GPUs is out of date, so the GPU server waits until Loomplane has counted again."},
		{cloudinfra.CapacityBlock{Reason: "expansion_approval", Resource: "policy", Retryable: true}, true,
			"The GPU server waits for an admin to approve it in the cloud's capacity policy."},
		{cloudinfra.CapacityBlock{Reason: "hard_limit", Resource: "gpus:nvidia-h100-80gb", Requested: 1}, false,
			"The cloud's capacity policy has no ceiling for NVIDIA H100 80GB GPUs, so it allows none. An admin can add one, then deploy the model again."},
		{cloudinfra.CapacityBlock{Reason: "hard_limit", Resource: "servers", Limit: 3, Counted: 3, Requested: 1}, false,
			"The GPU server would take the cloud past its capacity limit of 3 servers: 3 are counted and it needs 1 more. An admin can raise the limit, then deploy the model again."},
		{cloudinfra.CapacityBlock{Reason: "provider_quota", Resource: "servers", Limit: 10, Counted: 10, Requested: 1}, false,
			"The DigitalOcean account's quota has no room for the GPU server's servers: it allows 10, 10 are counted and it needs 1 more."},
		// A reason this page has no sentence of its own for still reads as
		// words, never as codes.
		{cloudinfra.CapacityBlock{Reason: "placement_constraint", Resource: "pool"}, false,
			"The cloud's capacity policy holds the GPU server back: no pool can place it."},
	} {
		if got := capacitySentence(c.block); got != c.want {
			t.Errorf("%s %s: %q", c.block.Reason, c.block.Resource, got)
		}
		if capacityPaused(c.block) != c.paused {
			t.Errorf("%s paused %v", c.block.Reason, !c.paused)
		}
	}
}

func TestServerNameForIncludesTheEnvironment(t *testing.T) {
	if got := serverNameFor("shop", "llm", "production"); got != "shop-llm-production-gpu" {
		t.Errorf("short: %q", got)
	}
	for _, long := range []string{
		serverNameFor(strings.Repeat("p", 70), "llm", "production"),
		serverNameFor(strings.Repeat("p", 40), strings.Repeat("r", 40), "staging"),
	} {
		if len(long) > 63 || !strings.HasSuffix(long, "-gpu") || strings.Contains(long, "--") {
			t.Errorf("long: %q (%d)", long, len(long))
		}
	}
	// Final review M7: the longest slug (48) and resource (40) keep the
	// environment, so two environments' servers never fall back to "-gpu"
	// and "-gpu-2".
	slug, resource := "s"+strings.Repeat("-a", 23)+"z", strings.Repeat("r", 40)
	names := map[string]bool{}
	for _, env := range []string{"staging", "production", "preview-" + strings.Repeat("e", 24)} {
		name := serverNameFor(slug, resource, env)
		if len(name) > 63 || !strings.HasSuffix(name, "-"+env+"-gpu") || strings.Contains(name, "--") || !strings.HasPrefix(name, "s-a") ||
			!strings.Contains(name, "-rrrr") {
			t.Errorf("%s: %q (%d)", env, name, len(name))
		}
		names[name] = true
	}
	if len(names) != 3 {
		t.Errorf("names %v", names)
	}
	// A short slug leaves the resource the rest.
	if got := serverNameFor("shop", strings.Repeat("r", 60), "staging"); got != "shop-"+strings.Repeat("r", 63-len("shop--staging-gpu"))+"-staging-gpu" {
		t.Errorf("short slug: %q", got)
	}
}

func TestHourlyText(t *testing.T) {
	for usd, want := range map[float64]string{1.57: "1.57", 3.44: "3.44", 0.0298: "0.0298", 12: "12.00"} {
		if got := hourlyText(usd); got != want {
			t.Errorf("%v: %q", usd, got)
		}
	}
}

// Ruling M28: an apply that fails after the accept went through, a race
// lost to the cron or an error on the way back, keeps the row. Deleting it
// would leave a server Loomplane is creating with nothing pointing at it,
// and a retry would buy a second.
func TestDeployKeepsTheRowOfAnApprovedServer(t *testing.T) {
	ctx := context.Background()
	for name, late := range map[string]error{
		"lost the race":      afterAcceptError{cloudinfra.ErrConflict},
		"error after commit": afterAcceptError{errors.New("connection reset by peer")},
	} {
		sf := newServerFirst(t, cloudAdmin)
		sf.servers.applyErrs = []error{late}
		pv, res := sf.deployOnNewServer(t)
		d := sf.row(t)
		if res.Deployment == nil || d.Phase != PhaseWaitingForServer || d.ServerOperationID != pv.Server.ID || d.Message != "" {
			t.Errorf("%s: row %+v", name, d)
		}
		// The cron carries it on as a server on its way, never approving it again.
		if err := sf.svc.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		if _, applies, _ := sf.servers.calls(); applies != 1 || sf.row(t).Phase != PhaseWaitingForServer {
			t.Errorf("%s: %d applies, %s", name, applies, sf.row(t).Phase)
		}
	}

	// An error with the review still in preview is a real failure: the
	// row goes and the deploy says why.
	sf := newServerFirst(t, cloudAdmin)
	sf.servers.applyErrs = []error{errors.New("the database is down")}
	in := DeployInput{EntryID: qwen7B, Target: onSize(), ReviewServer: true}
	pv, err := sf.svc.Preview(ctx, member, projectID, stagingID, in)
	if err != nil {
		t.Fatal(err)
	}
	in.ReviewServer, in.Server = false, &ServerApproval{CloudID: cloudID, OperationID: pv.Server.ID, Review: pv.Server.Review}
	if _, err = sf.svc.Deploy(ctx, member, projectID, stagingID, in); err == nil || len(sf.rows.rows) != 0 {
		t.Errorf("failed apply: %v, %d rows", err, len(sf.rows.rows))
	}
}

// Ruling M28: the cron approves a review again only for a row that
// recorded a capacity pause. One without is a deploy still applying.
func TestReconcileLeavesAnInFlightDeployAlone(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	in := DeployInput{EntryID: qwen7B, Target: onSize(), ReviewServer: true}
	pv, err := sf.svc.Preview(ctx, member, projectID, stagingID, in)
	if err != nil {
		t.Fatal(err)
	}
	// The row as Deploy writes it, just before it applies the review.
	in.ReviewServer, in.Server = false, &ServerApproval{CloudID: cloudID, OperationID: pv.Server.ID, Review: pv.Server.Review}
	in.Resource, in.Quantization = "qwen2-5-7b-instruct", "bf16"
	row := &Deployment{OrgID: "org", ProjectID: projectID, EnvironmentID: stagingID, Resource: "qwen2-5-7b-instruct", EntryID: qwen7B,
		Quantization: "bf16", Engine: "vllm", Phase: PhaseWaitingForServer, ServerCloudID: cloudID, ServerOperationID: pv.Server.ID,
		ServerName: serverName, Request: &in, RequestedBy: member}
	if err = sf.rows.Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err = sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, applies, _ := sf.servers.calls(); applies != 0 || sf.row(t).Phase != PhaseWaitingForServer {
		t.Errorf("the cron raced the deploy: %d applies, %s", applies, sf.row(t).Phase)
	}
	// A deploy that never applied before its review expired needs attention.
	sf.advance(16 * time.Minute)
	if err = sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if d := sf.row(t); d.Phase != PhaseNeedsAttention || !strings.Contains(d.Message, "never approved") {
		t.Errorf("expired: %s %q", d.Phase, d.Message)
	}
	if _, applies, _ := sf.servers.calls(); applies != 0 {
		t.Errorf("%d applies", applies)
	}
}

// Ruling M28: two cron runs approving the same paused review: the one that
// loses sees the server on its way and keeps waiting.
func TestOverlappingReconcilesKeepWaiting(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	sf.servers.applyErrs = []error{staleCount(), afterAcceptError{cloudinfra.ErrConflict}}
	sf.deployOnNewServer(t)
	if d := sf.row(t); d.Message == "" {
		t.Fatal("the pause wasn't recorded")
	}
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	d := sf.row(t)
	if d.Phase != PhaseWaitingForServer || d.Message != "" {
		t.Errorf("lost the race: %s %q", d.Phase, d.Message)
	}
	// With the server on its way, later runs don't approve it again.
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, applies, _ := sf.servers.calls(); applies != 2 {
		t.Errorf("%d applies", applies)
	}
}

// Ruling M28: cloudinfra won't accept a review whose capacity reading is
// more than five minutes old, and approving it again never refreshes it.
// That's a refusal, not a pause.
func TestAStaleReviewNeedsANewOne(t *testing.T) {
	ctx := context.Background()
	staleRequest := &cloudinfra.CapacityAdmissionError{Block: cloudinfra.CapacityBlock{Reason: "stale_inventory", Resource: "request", Retryable: true}}

	// At deploy.
	sf := newServerFirst(t, cloudAdmin)
	sf.servers.applyErrs = []error{staleRequest}
	in := DeployInput{EntryID: qwen7B, Target: onSize(), ReviewServer: true}
	pv, err := sf.svc.Preview(ctx, member, projectID, stagingID, in)
	if err != nil {
		t.Fatal(err)
	}
	in.ReviewServer, in.Server = false, &ServerApproval{CloudID: cloudID, OperationID: pv.Server.ID, Review: pv.Server.Review}
	var refused *ProblemError
	if _, err = sf.svc.Deploy(ctx, member, projectID, stagingID, in); !errors.As(err, &refused) || err.Error() != staleReview {
		t.Errorf("deploy: %v", err)
	}
	// Deploy checks the reading's age itself before it writes anything.
	sf.servers.set(pv.Server.ID, func(o *cloudinfra.Operation) { o.CapacityCheckedAt = sf.now.Add(-6 * time.Minute) })
	if _, err = sf.svc.Deploy(ctx, member, projectID, stagingID, in); !errors.As(err, &refused) || refused.Problem.Code != ProblemServerReview ||
		err.Error() != staleReview {
		t.Errorf("old review: %v", err)
	}
	if len(sf.rows.rows) != 0 {
		t.Error("a stale review left a row")
	}

	// In the cron, after a pause.
	sf = newServerFirst(t, cloudAdmin)
	sf.servers.applyErrs = []error{staleCount(), staleRequest}
	sf.deployOnNewServer(t)
	sf.advance(6 * time.Minute)
	if err = sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if d := sf.row(t); d.Phase != PhaseNeedsAttention || d.Message != staleReview {
		t.Errorf("cron: %s %q", d.Phase, d.Message)
	}
}

// Ruling M28: a stranded row its requester can no longer read says so,
// which takes it off the list the cron reads every minute.
func TestStrandedRowItsRequesterCantRead(t *testing.T) {
	ctx := context.Background()
	authz := &byUser{users: map[string]perms{"usr_1": cloudAdmin}}
	sf := newServerFirst(t, authz)
	gone := security.Principal{UserID: "usr_left", OrgID: "org"}
	d := &Deployment{OrgID: "org", ProjectID: projectID, EnvironmentID: stagingID, Resource: "bge", EntryID: bgeSmall,
		Quantization: "fp32", Engine: "tei", Phase: PhasePlanned, RequestedBy: gone}
	if err := sf.rows.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	sf.advance(StrandedAfter + time.Minute)
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := sf.rows.Get(ctx, "org", projectID, stagingID, "bge")
	if err != nil || !strings.Contains(got.Message, "can't read the project any more") {
		t.Fatalf("row %+v, %v", got, err)
	}
	if left, _ := sf.rows.Stranded(ctx, sf.now.Add(time.Hour)); len(left) != 0 {
		t.Errorf("still stranded: %+v", left)
	}
}

// A server-first row the cron marked planned, then stopped before the
// edit, has a server that's up: it goes back to waiting, never away, and
// the next run plans the model onto the server.
func TestStrandedServerFirstRowWaitsAgain(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	pv, _ := sf.deployOnNewServer(t)
	sf.servers.succeed(pv.Server.ID)
	sf.gpusReady(provider.GPUStatusReady)
	d := sf.row(t)
	d.Phase = PhasePlanned
	if err := sf.rows.Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	sf.advance(StrandedAfter + time.Minute)
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if d = sf.row(t); d.Phase != PhaseWaitingForServer {
		t.Fatalf("stranded server-first row: %s", d.Phase)
	}
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if d = sf.row(t); d.Phase != PhasePlanned || d.PlanID == "" || len(sf.plans.asked) != 1 {
		t.Errorf("next run: %s plan %q", d.Phase, d.PlanID)
	}
}

// Ruling M29: someone cancels a deployment whose server the capacity
// policy paused, while a cron run approves that same review. The row goes
// either way; the cancel says the server is coming and where to delete it,
// and the log says so too.
func TestCancelSaysTheServerIsAlreadyComing(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	var warned []string
	sf.svc.d.Warn = func(msg string, err error) { warned = append(warned, msg+": "+err.Error()) }
	sf.servers.applyErrs = []error{staleCount()}
	sf.deployOnNewServer(t)

	// Still paused: nothing was approved, so there's nothing to say.
	res, err := sf.svc.CancelWaiting(ctx, member, projectID, stagingID, "qwen2-5-7b-instruct")
	if err != nil || res.Message != "" || len(warned) != 0 {
		t.Fatalf("paused cancel: %+v, %v, warned %v", res, err, warned)
	}

	// A cron run approved the review by the time the cancel reads it again.
	sf.servers.applyErrs = []error{staleCount()}
	pv, _ := sf.deployOnNewServer(t)
	sf.servers.set(pv.Server.ID, func(op *cloudinfra.Operation) { op.Status = "pending" })
	res, err = sf.svc.CancelWaiting(ctx, member, projectID, stagingID, "qwen2-5-7b-instruct")
	if err != nil {
		t.Fatal(err)
	}
	want := serverName + " was already approved and is being created. Delete it in Infrastructure if you don't want it: it costs money once it's up."
	if res.Message != want {
		t.Errorf("cancel message %q", res.Message)
	}
	if _, err = sf.rows.Get(ctx, "org", projectID, stagingID, "qwen2-5-7b-instruct"); !errors.Is(err, ErrNotFound) {
		t.Errorf("row after cancel: %v", err)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], pv.Server.ID) || !strings.Contains(warned[0], serverName) {
		t.Errorf("warned %v", warned)
	}

	// Someone who can't read the cloud's servers is told to ask an admin.
	dev := newServerFirst(t, cloudAdmin)
	dev.servers.applyErrs = []error{staleCount()}
	pv, _ = dev.deployOnNewServer(t)
	dev.servers.set(pv.Server.ID, func(op *cloudinfra.Operation) { op.Status = "pending" })
	dev.svc.d.Authz, dev.servers.authz = deployer, deployer
	res, err = dev.svc.CancelWaiting(ctx, member, projectID, stagingID, "qwen2-5-7b-instruct")
	if err != nil || !strings.HasPrefix(res.Message, serverName+" may already be on its way. Ask an admin to check Infrastructure") {
		t.Errorf("developer cancel: %+v, %v", res, err)
	}
}

// The other side of the race: the cron's approval goes through after the
// cancel took the row. The server is coming with nothing waiting for it,
// so the cron logs it, and the run doesn't fail.
func TestApprovalAfterCancelIsLogged(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	var warned []string
	sf.svc.d.Warn = func(msg string, err error) { warned = append(warned, msg+": "+err.Error()) }
	sf.servers.applyErrs = []error{staleCount()}
	pv, _ := sf.deployOnNewServer(t)
	sf.servers.onApply = func() {
		if err := sf.rows.Delete(ctx, "org", projectID, stagingID, "qwen2-5-7b-instruct"); err != nil {
			t.Error(err)
		}
	}
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "was cancelled while its GPU server was being approved") || !strings.Contains(warned[0], pv.Server.ID) {
		t.Errorf("warned %v", warned)
	}
}

// qwenOnGPUBox deploys Qwen 7B onto gpu-box in staging and says it runs
// there.
func (sf *serverFirstSetup) qwenOnGPUBox(t *testing.T) {
	t.Helper()
	sf.targets.targets = append(sf.targets.targets, l40sServer())
	res, err := sf.svc.Deploy(context.Background(), member, projectID, stagingID, DeployInput{EntryID: qwen7B, Target: onGPU("nvidia-l40s")})
	if err != nil {
		t.Fatal(err)
	}
	r := current(t, sf.deploySetup).Resources[res.Deployment.Resource]
	sf.facts.running(stagingID, res.Deployment.Resource, r.Kind, r.Settings, 1)
}

// moveOntoNewServer reviews a new L40S server for Qwen 7B and moves it
// there, with the review approved.
func (sf *serverFirstSetup) moveOntoNewServer(t *testing.T) (*DeployPreview, error) {
	t.Helper()
	ctx := context.Background()
	in := MoveInput{Target: onSize(), ReviewServer: true}
	pv, err := sf.svc.MovePreview(ctx, member, projectID, stagingID, "qwen2-5-7b-instruct", in)
	if err != nil || len(pv.Problems) != 0 || pv.Server == nil || !pv.CanDeploy {
		t.Fatalf("move preview: %+v, %v", pv, err)
	}
	in.ReviewServer, in.Server = false, &ServerApproval{CloudID: pv.Server.CloudID, OperationID: pv.Server.ID, Review: pv.Server.Review}
	_, err = sf.svc.Move(ctx, member, projectID, stagingID, "qwen2-5-7b-instruct", in)
	return pv, err
}

// A move onto a GPU server Loomplane creates first waits for it, as a
// deploy does, with the model running where it was until then. Nothing
// approves a second server for it, and once the server is up the cron
// moves it.
func TestMoveOntoANewServerWaitsForIt(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	sf.qwenOnGPUBox(t)
	revisions := sf.revisions.count()
	pv, err := sf.moveOntoNewServer(t)
	if err != nil {
		t.Fatal(err)
	}
	d := sf.row(t)
	if d.Phase != PhaseWaitingForServer || d.ServerOperationID != pv.Server.ID || d.ServerName != serverName || d.Request == nil ||
		d.Request.Moving == nil || d.Request.Moving.ServerName != "gpu-box" || d.Request.Moving.Phase != PhasePlanned {
		t.Fatalf("moving row %+v, request %+v", d, d.Request)
	}
	if sf.revisions.count() != revisions || current(t, sf.deploySetup).Resources[d.Resource].Target != gpuConn {
		t.Error("the manifest moved before the server was up")
	}
	if st, serr := sf.svc.Status(ctx, member, projectID, stagingID, d.Resource); serr != nil || st.State != StateWaitingForServer {
		t.Errorf("status while moving: %+v, %v", st, serr)
	}
	// A second move, or removing it, waits for this one.
	if _, err = sf.svc.Move(ctx, member, projectID, stagingID, d.Resource, MoveInput{Target: onSize(), Server: &ServerApproval{CloudID: cloudID, OperationID: pv.Server.ID, Review: pv.Server.Review}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("second move: %v", err)
	}
	if _, err = sf.svc.Remove(ctx, member, projectID, stagingID, d.Resource); !errors.Is(err, ErrInvalid) {
		t.Errorf("remove while moving: %v", err)
	}
	if previews, applies, _ := sf.servers.calls(); previews != 1 || applies != 1 {
		t.Errorf("%d reviews, %d applies", previews, applies)
	}
	// It still serves where it is while it moves.
	if got, _, eerr := sf.svc.Endpoints(ctx, "org", projectID, Placement{EnvironmentID: stagingID, ConnectionID: gpuConn}); eerr != nil || len(got) != 1 || got[0].Resource != d.Resource {
		t.Errorf("endpoints while moving: %+v, %v", got, eerr)
	}

	sf.servers.succeed(pv.Server.ID)
	sf.gpusReady(provider.GPUStatusReady)
	if err = sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	r := current(t, sf.deploySetup).Resources[d.Resource]
	if r.Target != newConn || r.GPU == nil || r.GPU.Kind != "nvidia-l40s" || !enabledIn(r, "staging") {
		t.Errorf("moved resource %+v", r)
	}
	d = sf.row(t)
	if d.Phase != PhasePlanned || d.PlanID == "" || d.Message != "" || d.ServerName != serverName {
		t.Errorf("row after the move %+v", d)
	}
	if sf.gateways.count() != 1 {
		t.Errorf("gateways told %d times", sf.gateways.count())
	}
	if previews, applies, _ := sf.servers.calls(); previews != 1 || applies != 1 {
		t.Errorf("%d reviews, %d applies after the move", previews, applies)
	}
}

// Review Focus 3: staging's copy moving onto a new GPU server waits for
// one server, and once it's up moves staging's copy alone. Production's
// copy, row and plan stay where they are.
func TestMoveOneCopyOntoANewServer(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	sf.qwenOnGPUBox(t)
	res, err := sf.svc.Deploy(ctx, member, projectID, productID, DeployInput{EntryID: qwen7B, Target: onGPU("nvidia-l40s")})
	if err != nil || res.Deployment.Resource != "qwen2-5-7b-instruct" {
		t.Fatalf("production: %+v, %v", res, err)
	}
	production, _ := sf.rows.Get(ctx, "org", projectID, productID, "qwen2-5-7b-instruct")
	pv, err := sf.moveOntoNewServer(t)
	if err != nil {
		t.Fatal(err)
	}
	sf.servers.succeed(pv.Server.ID)
	sf.gpusReady(provider.GPUStatusReady)
	if err = sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	m := current(t, sf.deploySetup)
	r := m.Resources["qwen2-5-7b-instruct"]
	if r.For("staging", m.Defaults).Target != newConn || r.For("production", m.Defaults).Target != gpuConn || !enabledIn(r, "production") {
		t.Errorf("after staging's move %+v", r)
	}
	if after, _ := sf.rows.Get(ctx, "org", projectID, productID, "qwen2-5-7b-instruct"); after.Revision != production.Revision || after.PlanID != production.PlanID {
		t.Errorf("production's row %+v", after)
	}
	if previews, applies, _ := sf.servers.calls(); previews != 1 || applies != 1 {
		t.Errorf("%d reviews, %d applies", previews, applies)
	}
	if last := sf.plans.asked[len(sf.plans.asked)-1]; last.Environment != "staging" {
		t.Errorf("plans %+v", sf.plans.asked)
	}
}

// Cancelling a move puts the model back as it was, and says the server
// it approved is coming. A move whose server the capacity policy refuses
// puts it back too.
func TestCancelledMoveKeepsTheModelWhereItWas(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	sf.qwenOnGPUBox(t)
	before := sf.row(t)
	if _, err := sf.moveOntoNewServer(t); err != nil {
		t.Fatal(err)
	}
	res, err := sf.svc.CancelWaiting(ctx, member, projectID, stagingID, before.Resource)
	if err != nil || !strings.Contains(res.Message, "was already approved and is being created") {
		t.Fatalf("cancel: %+v, %v", res, err)
	}
	d := sf.row(t)
	if d.Phase != before.Phase || d.ServerName != "gpu-box" || d.ServerOperationID != "" || d.PlanID != before.PlanID || d.Request != nil {
		t.Errorf("row after cancel %+v", d)
	}
	if current(t, sf.deploySetup).Resources[d.Resource].Target != gpuConn {
		t.Error("the cancel moved the model")
	}

	sf.servers.applyErrs = []error{gpuCeiling()}
	_, err = sf.moveOntoNewServer(t)
	var refused *ProblemError
	if !errors.As(err, &refused) || refused.Problem.Code != ProblemCapacity {
		t.Fatalf("refused move: %v", err)
	}
	if d = sf.row(t); d.Phase != before.Phase || d.ServerName != "gpu-box" || d.Request != nil {
		t.Errorf("row after a refused move %+v", d)
	}
}

// The cron stopped a move between marking it planned and its plan. The
// next run puts it back to waiting, never deletes it, and the one after
// finishes the move.
func TestStrandedMoveFinishes(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	sf.qwenOnGPUBox(t)
	pv, err := sf.moveOntoNewServer(t)
	if err != nil {
		t.Fatal(err)
	}
	sf.servers.succeed(pv.Server.ID)
	sf.gpusReady(provider.GPUStatusReady)
	d := sf.row(t)
	d.Phase, d.PlanID = PhasePlanned, ""
	if err = sf.rows.Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	sf.advance(StrandedAfter + time.Minute)
	if err = sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if d = sf.row(t); d.Phase != PhaseWaitingForServer {
		t.Fatalf("stranded move: %+v", d)
	}
	if err = sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if d = sf.row(t); d.Phase != PhasePlanned || d.PlanID == "" || current(t, sf.deploySetup).Resources[d.Resource].Target != newConn {
		t.Errorf("finished move: %+v", d)
	}
}

// onItsNewServer deploys Qwen 7B onto a new L40S server and carries the
// deploy on until it's planned there.
func (sf *serverFirstSetup) onItsNewServer(t *testing.T) *DeployPreview {
	t.Helper()
	pv, _ := sf.deployOnNewServer(t)
	sf.servers.succeed(pv.Server.ID)
	sf.gpusReady(provider.GPUStatusReady)
	if err := sf.svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := sf.row(t); d.Phase != PhasePlanned || d.ServerCloudID != cloudID || d.ServerName != serverName {
		t.Fatalf("on its new server: %+v", d)
	}
	return pv
}

// Ruling M30: moving off a GPU server Loomplane created says the server
// keeps billing, and a move onto another new server never reuses its
// name.
func TestMoveOffALoomplaneServerSaysItStays(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	sf.onItsNewServer(t)
	sf.targets.targets = append(sf.targets.targets, l40sServer())
	res, err := sf.svc.Move(ctx, member, projectID, stagingID, "qwen2-5-7b-instruct", MoveInput{Target: onGPU("nvidia-l40s")})
	if err != nil {
		t.Fatal(err)
	}
	want := serverName + " keeps running and costs money until you delete it in Infrastructure."
	d := sf.row(t)
	if res.Message != want || d.Message != want || d.ServerName != "gpu-box" || d.ServerCloudID != "" {
		t.Errorf("result %q, row %+v", res.Message, d)
	}

	// The next new server gets a name of its own.
	pv, err := sf.svc.MovePreview(ctx, member, projectID, stagingID, "qwen2-5-7b-instruct", MoveInput{Target: onSize(), ReviewServer: true})
	if err != nil || pv.Server == nil || pv.TargetName != serverName+"-2" || pv.Server.Input.Name != serverName+"-2" {
		t.Errorf("second server: %q, %+v, %v", pv.TargetName, pv.Server, err)
	}
	if got := freeServerName(strings.Repeat("a", 59)+"-gpu", map[string]bool{strings.Repeat("a", 59) + "-gpu": true}); len(got) > 63 || !strings.HasSuffix(got, "-2") {
		t.Errorf("long name %q", got)
	}
}

// Removing a model from a server Loomplane created for it says the
// server stays and bills, as well as the weights.
func TestRemoveSaysALoomplaneServerStays(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	sf.onItsNewServer(t)
	res, err := sf.svc.Remove(ctx, member, projectID, stagingID, "qwen2-5-7b-instruct")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Message, "as retained storage") || !strings.HasSuffix(res.Message, "Loomplane created "+serverName+" for this model, and it keeps running and costs money until you delete it in Infrastructure.") {
		t.Errorf("message %q", res.Message)
	}
}

// Ruling M30: a model waiting for its new server costs what the size
// costs, and an unknown price stays unknown.
func TestWaitingModelShowsTheServersPrice(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	sf.deployOnNewServer(t)
	st, err := sf.svc.Status(ctx, member, projectID, stagingID, "qwen2-5-7b-instruct")
	if err != nil || st.State != StateWaitingForServer || st.Cost.Source != CostSourceServer || st.Cost.HourlyUSD != "1.5700" || st.ProjectedUSD == "" {
		t.Fatalf("waiting: %+v, %v", st, err)
	}
	sf.targets.targets[0].HourlyPriceKnown, sf.targets.targets[0].HourlyUSD = false, 0
	if st, err = sf.svc.Status(ctx, member, projectID, stagingID, "qwen2-5-7b-instruct"); err != nil ||
		st.Cost.Source != CostSourceUnknown || st.Cost.HourlyUSD != "" || st.ProjectedUSD != "" {
		t.Errorf("unknown price: %+v, %v", st.Cost, err)
	}
}

// A plain MovePreview onto a server size writes nothing: no review, no
// row change, no manifest revision.
func TestPlainMovePreviewWritesNothing(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	sf.qwenOnGPUBox(t)
	before, revisions := sf.row(t), sf.revisions.count()
	pv, err := sf.svc.MovePreview(ctx, member, projectID, stagingID, before.Resource, MoveInput{Target: onSize()})
	if err != nil || !pv.ServerReviewNeeded || pv.CanDeploy || pv.Server != nil {
		t.Fatalf("plain move preview: %+v, %v", pv, err)
	}
	previews, applies, _ := sf.servers.calls()
	if after := sf.row(t); previews != 0 || applies != 0 || after.Revision != before.Revision || sf.revisions.count() != revisions {
		t.Errorf("wrote: %d reviews, %d applies, row %d to %d, %d revisions", previews, applies, before.Revision, after.Revision, sf.revisions.count())
	}
}

// reviewIn reviews a new L40S server for Qwen 7B in env, and returns the
// deploy that approves it.
func (sf *serverFirstSetup) reviewIn(t *testing.T, env string) (*DeployPreview, DeployInput) {
	t.Helper()
	in := DeployInput{EntryID: qwen7B, Target: onSize(), ReviewServer: true}
	pv, err := sf.svc.Preview(context.Background(), member, projectID, env, in)
	if err != nil || len(pv.Problems) != 0 || pv.Server == nil {
		t.Fatalf("review in %s: %+v, %v", env, pv, err)
	}
	in.ReviewServer = false
	in.Server = &ServerApproval{CloudID: pv.Server.CloudID, OperationID: pv.Server.ID, Review: pv.Server.Review}
	return pv, in
}

// Ruling M31: a server that's approved but not created yet has no server
// row, so its name is taken by the model waiting for it and by its open
// create. A review only previewed takes no name.
func TestServersOnTheirWayKeepTheirNames(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	sf.deployOnNewServer(t)
	if len(sf.servers.servers) != 0 {
		t.Fatal("the fake made a server row before the create started")
	}
	// Staging's waiting model is the same model, so production's copy has
	// its name, and its server the environment's name.
	const production = "shop-qwen2-5-7b-instruct-production-gpu"
	pv, _ := sf.reviewIn(t, productID)
	if pv.Resource != "qwen2-5-7b-instruct" || pv.TargetName != production || pv.Server.Input.Name != production {
		t.Fatalf("production's model %q, server %q, review %q", pv.Resource, pv.TargetName, pv.Server.Input.Name)
	}
	// Another model waiting for a server of that name takes it.
	other := &Deployment{OrgID: "org", ProjectID: projectID, EnvironmentID: stagingID, Resource: "other-model", EntryID: llama8BID,
		Quantization: "bf16", Engine: "vllm", Phase: PhaseWaitingForServer, ServerCloudID: cloudID, ServerOperationID: "infraop_other",
		ServerName: production}
	if err := sf.rows.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	if pv, _ = sf.reviewIn(t, productID); pv.TargetName != production+"-2" || pv.Server.Input.Name != production+"-2" {
		t.Fatalf("beside a waiting model: server %q, review %q", pv.TargetName, pv.Server.Input.Name)
	}
	// Reviewing again keeps the name: the first review is only previewed.
	if pv, _ = sf.reviewIn(t, productID); pv.Server.Input.Name != production+"-2" {
		t.Errorf("second review %q", pv.Server.Input.Name)
	}
	// A create approved in Infrastructure, with no model row, takes its
	// name too until it's done.
	running := pv.Server.ID
	sf.servers.set(running, func(op *cloudinfra.Operation) { op.Status = "running" })
	if pv, _ = sf.reviewIn(t, productID); pv.Server.Input.Name != production+"-3" {
		t.Errorf("review beside a running create %q", pv.Server.Input.Name)
	}
	// A failed create can be retried, so it keeps its name; once it's
	// cancelled the name is free again.
	sf.servers.set(running, func(op *cloudinfra.Operation) { op.Status = "failed" })
	if pv, _ = sf.reviewIn(t, productID); pv.Server.Input.Name != production+"-3" {
		t.Errorf("review beside a failed create %q", pv.Server.Input.Name)
	}
	sf.servers.set(running, func(op *cloudinfra.Operation) { op.Status = "cancelled" })
	if pv, _ = sf.reviewIn(t, productID); pv.Server.Input.Name != production+"-2" {
		t.Errorf("review once the other creates ended %q", pv.Server.Input.Name)
	}
}

// A model waiting for its GPU server keeps its name. Another model can't
// take it, the same model deployed to another environment meanwhile is
// another copy of it, and the waiting model adds its environment to that
// resource once its server is up (Review Focus 2).
func TestWaitingModelKeepsItsName(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	pv, _ := sf.deployOnNewServer(t)
	sf.targets.targets = append(sf.targets.targets, l40sServer())
	// The name staging's model waits with is a model's in this project,
	// even with no manifest resource yet.
	named := DeployInput{EntryID: llama8BID, Target: onGPU("nvidia-l40s"), Resource: "qwen2-5-7b-instruct"}
	_, err := sf.svc.Deploy(ctx, member, projectID, productID, named)
	var refused *ProblemError
	if !errors.As(err, &refused) || refused.Problem.Code != ProblemNameTaken ||
		err.Error() != "qwen2-5-7b-instruct is already a model in this project. Pick another name." {
		t.Errorf("taking the waiting model's name: %v", err)
	}
	res, err := sf.svc.Deploy(ctx, member, projectID, productID, DeployInput{EntryID: qwen7B, Target: onGPU("nvidia-l40s")})
	if err != nil || res.Deployment.Resource != "qwen2-5-7b-instruct" {
		t.Fatalf("production on gpu-box: %+v, %v", res, err)
	}
	// The same entry in staging again is the one on its way.
	again, err := sf.svc.Preview(ctx, member, projectID, stagingID, DeployInput{EntryID: qwen7B, Target: onGPU("nvidia-l40s")})
	if err != nil || !slices.Equal(codes(again), []string{ProblemExists}) ||
		again.Problems[0].Message != "qwen2-5-7b-instruct is already deploying Qwen2.5 7B Instruct in staging." {
		t.Errorf("staging again: %+v, %v", again, err)
	}

	sf.servers.succeed(pv.Server.ID)
	sf.gpusReady(provider.GPUStatusReady)
	if err = sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if d := sf.row(t); d.Phase != PhasePlanned || d.PlanID == "" || d.Message != "" {
		t.Fatalf("staging once its server is up: %+v", d)
	}
	m := current(t, sf.deploySetup)
	r := m.Resources["qwen2-5-7b-instruct"]
	if len(m.Resources) != 1 || r.Target != gpuConn || !enabledIn(r, "production") || !enabledIn(r, "staging") ||
		r.For("staging", m.Defaults).Target != newConn {
		t.Errorf("resources %+v", m.Resources)
	}
}

// Ruling E3: a copy waiting for its GPU server in staging has no manifest
// resource yet, and a deploy of the same model to production joins it all
// the same: its name, and its kind, so a target that would run another
// kind is refused before anything is spent.
func TestDeployJoinsAWaitingCopy(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	in := DeployInput{EntryID: qwen7B, Resource: "llm", Target: onSize(), ReviewServer: true}
	review, err := sf.svc.Preview(ctx, member, projectID, stagingID, in)
	if err != nil || len(review.Problems) != 0 || review.Server == nil {
		t.Fatalf("staging's review: %+v, %v", review, err)
	}
	in.ReviewServer, in.Server = false, &ServerApproval{CloudID: review.Server.CloudID, OperationID: review.Server.ID, Review: review.Server.Review}
	if _, err = sf.svc.Deploy(ctx, member, projectID, stagingID, in); err != nil {
		t.Fatal(err)
	}
	sf.targets.targets = append(sf.targets.targets, l40sServer())
	*sf.conns = append(*sf.conns, cpuServer())

	gpu := DeployInput{EntryID: qwen7B, Target: onGPU("nvidia-l40s")}
	pv, err := sf.svc.Preview(ctx, member, projectID, productID, gpu)
	if err != nil || len(pv.Problems) != 0 || pv.Resource != "llm" || pv.AddsTo != "llm" || pv.Kind != recipes.ModelVLLMKind {
		t.Fatalf("production on gpu-box: %q adds to %q as %q %+v, %v", pv.Resource, pv.AddsTo, pv.Kind, pv.Problems, err)
	}
	want := []Reach{
		{EnvironmentID: productID, Environment: "production", Mode: ReachHome, Message: "Adds production to llm. Services in production link to this copy."},
		{EnvironmentID: stagingID, Environment: "staging", Mode: ReachHome, Message: "Waits for its GPU server in staging. Services in staging link to this copy once it runs."},
	}
	if !slices.Equal(pv.Environments, want) {
		t.Errorf("environments %+v", pv.Environments)
	}

	// On CPU it would run as llama.cpp: refused, with nothing written.
	previews, applies, _ := sf.servers.calls()
	revisions := sf.revisions.count()
	cpu := DeployInput{EntryID: qwen7B, Target: TargetChoice{Source: TargetConnection, ConnectionID: cpuConn}}
	pv, err = sf.svc.Preview(ctx, member, projectID, productID, cpu)
	wantKind := "llm runs as vLLM model in its other environments, and on cpu-box it would run as llama.cpp model. Pick a target it runs on as vLLM model, or deploy it as a separately named model."
	if err != nil || !slices.Equal(codes(pv), []string{ProblemEngine}) || pv.Problems[0].Message != wantKind || pv.CanDeploy {
		t.Errorf("production on cpu-box: %+v, %v", pv.Problems, err)
	}
	var refused *ProblemError
	if _, err = sf.svc.Deploy(ctx, member, projectID, productID, cpu); !errors.As(err, &refused) || refused.Problem.Code != ProblemEngine {
		t.Errorf("deploy on cpu-box: %v", err)
	}
	if p, a, _ := sf.servers.calls(); p != previews || a != applies || sf.revisions.count() != revisions || len(sf.rows.rows) != 1 {
		t.Errorf("the refusal wrote: %d reviews, %d applies, %d revisions, %d rows", p, a, sf.revisions.count(), len(sf.rows.rows))
	}

	// On gpu-box it joins, and staging's server-up joins production's.
	res, err := sf.svc.Deploy(ctx, member, projectID, productID, gpu)
	if err != nil || res.Deployment.Resource != "llm" {
		t.Fatalf("production on gpu-box: %+v, %v", res, err)
	}
	sf.servers.succeed(review.Server.ID)
	sf.gpusReady(provider.GPUStatusReady)
	if err = sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if d, gerr := sf.rows.Get(ctx, "org", projectID, stagingID, "llm"); gerr != nil || d.Phase != PhasePlanned || d.PlanID == "" {
		t.Errorf("staging once its server is up: %+v, %v", d, gerr)
	}
	m := current(t, sf.deploySetup)
	if r := m.Resources["llm"]; len(m.Resources) != 1 || !enabledIn(r, "staging") || !enabledIn(r, "production") {
		t.Errorf("resources %+v", m.Resources)
	}
}

// The same model waiting for new GPU servers in two environments gets a
// server each, named for its environment, and once both are up the one
// resource runs in both.
func TestTwoWaitingCopiesGetAServerEach(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	staging, _ := sf.deployOnNewServer(t)
	production, in := sf.reviewIn(t, productID)
	if production.Resource != "qwen2-5-7b-instruct" || production.TargetName != "shop-qwen2-5-7b-instruct-production-gpu" {
		t.Fatalf("production's review: %q on %q", production.Resource, production.TargetName)
	}
	if _, err := sf.svc.Deploy(ctx, member, projectID, productID, in); err != nil {
		t.Fatal(err)
	}
	sf.servers.succeed(staging.Server.ID)
	sf.servers.succeed(production.Server.ID)
	sf.gpusReady(provider.GPUStatusReady)
	if err := sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{stagingID, productID} {
		d, err := sf.rows.Get(ctx, "org", projectID, env, "qwen2-5-7b-instruct")
		if err != nil || d.Phase != PhasePlanned || d.PlanID == "" || d.Message != "" {
			t.Errorf("%s: %+v, %v", env, d, err)
		}
	}
	m := current(t, sf.deploySetup)
	if r := m.Resources["qwen2-5-7b-instruct"]; len(m.Resources) != 1 || !enabledIn(r, "staging") || !enabledIn(r, "production") {
		t.Errorf("resources %+v", m.Resources)
	}
}

// Ruling M31: two deploys that reviewed the same name never both get it.
// The second to be approved is told to review again, and when both are
// approved at the same moment, the one that finds the other gives way
// before its server is approved.
func TestTwoDeploysNeverShareAServerName(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	staging, stagingIn := sf.reviewIn(t, stagingID)
	if staging.Server.Input.Name != serverName {
		t.Fatalf("review %q", staging.Server.Input.Name)
	}
	// Another model's deploy, approved first, waits for a server of the
	// name staging's review has.
	first := &Deployment{OrgID: "org", ProjectID: projectID, EnvironmentID: productID, Resource: "other-model", EntryID: llama8BID,
		Quantization: "bf16", Engine: "vllm", Phase: PhaseWaitingForServer, ServerCloudID: cloudID, ServerOperationID: "infraop_other",
		ServerName: serverName}
	if err := sf.rows.Create(ctx, first); err != nil {
		t.Fatal(err)
	}
	var refused *ProblemError
	if _, err := sf.svc.Deploy(ctx, member, projectID, stagingID, stagingIn); !errors.As(err, &refused) || refused.Problem.Code != ProblemServerReview {
		t.Fatalf("second deploy of the same name: %v", err)
	}
	if _, applies, _ := sf.servers.calls(); applies != 0 {
		t.Errorf("%d servers approved", applies)
	}
	production, _ := sf.reviewIn(t, productID)
	if production.Server.Input.Name == serverName {
		t.Errorf("production's review %q has staging's name", production.Server.Input.Name)
	}

	// Both pass the name check, then each writes its row. Production's
	// lands while staging's deploy is between its row and its approval.
	sf = newServerFirst(t, cloudAdmin)
	_, stagingIn = sf.reviewIn(t, stagingID)
	production, _ = sf.reviewIn(t, productID)
	sf.rows.onCreate = func() {
		sf.rows.onCreate = nil // the other deploy's own Create runs no hook
		other := &Deployment{OrgID: "org", ProjectID: projectID, EnvironmentID: productID, Resource: "qwen2-5-7b-instruct", EntryID: qwen7B,
			Quantization: "bf16", Engine: "vllm", Phase: PhaseWaitingForServer, ServerCloudID: cloudID,
			ServerOperationID: production.Server.ID, ServerName: serverName}
		if err := sf.rows.Create(ctx, other); err != nil {
			t.Error(err)
		}
	}
	_, err := sf.svc.Deploy(ctx, member, projectID, stagingID, stagingIn)
	if !errors.As(err, &refused) || refused.Problem.Code != ProblemServerReview || !strings.Contains(refused.Problem.Message, serverName) {
		t.Fatalf("deploy that lost the name: %v", err)
	}
	if _, gerr := sf.rows.Get(ctx, "org", projectID, stagingID, "qwen2-5-7b-instruct"); !errors.Is(gerr, ErrNotFound) {
		t.Errorf("the row that gave way stays: %v", gerr)
	}
	if _, applies, _ := sf.servers.calls(); applies != 0 {
		t.Errorf("%d servers approved after the clash", applies)
	}
}

// Ruling M31: a move off a server Loomplane created says that server
// keeps billing only once the new one is approved. A paused or refused
// move hasn't moved the model.
func TestOnlyAnApprovedMoveSaysItLeftItsServer(t *testing.T) {
	ctx := context.Background()
	move := func(t *testing.T, applyErrs ...error) (*serverFirstSetup, *DeployResult, error) {
		sf := newServerFirst(t, cloudAdmin)
		sf.onItsNewServer(t)
		sf.servers.applyErrs = applyErrs
		in := MoveInput{Target: onSize(), ReviewServer: true}
		pv, err := sf.svc.MovePreview(ctx, member, projectID, stagingID, "qwen2-5-7b-instruct", in)
		if err != nil || pv.Server == nil {
			t.Fatalf("move preview: %+v, %v", pv, err)
		}
		in.ReviewServer, in.Server = false, &ServerApproval{CloudID: pv.Server.CloudID, OperationID: pv.Server.ID, Review: pv.Server.Review}
		res, err := sf.svc.Move(ctx, member, projectID, stagingID, "qwen2-5-7b-instruct", in)
		return sf, res, err
	}
	left := serverName + " keeps running and costs money until you delete it in Infrastructure."

	_, res, err := move(t)
	if err != nil || res.Message != left {
		t.Errorf("approved move: %+v, %v", res, err)
	}
	sf, res, err := move(t, staleCount())
	if d := sf.row(t); err != nil || res.Message != "" || d.Phase != PhaseWaitingForServer || !strings.Contains(d.Message, "out of date") {
		t.Errorf("paused move: %+v, %v, row %+v", res, err, d)
	}
	if _, res, err = move(t, gpuCeiling()); err == nil || (res != nil && res.Message != "") {
		t.Errorf("refused move: %+v, %v", res, err)
	}
	if _, res, err = move(t, errors.New("the cloud is down")); err == nil || (res != nil && res.Message != "") {
		t.Errorf("failed move: %+v, %v", res, err)
	}
}

// Ruling M31: while a model moves onto a new server, its page shows what
// it costs where it runs and what the new server costs, since both bill
// once the new one is up. An unknown price stays unknown.
func TestMovingModelShowsBothServersPrices(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	sf.qwenOnGPUBox(t)
	st, err := sf.svc.Status(ctx, member, projectID, stagingID, "qwen2-5-7b-instruct")
	if err != nil || st.MoveCost != nil {
		t.Fatalf("before the move: %+v, %v", st, err)
	}
	if _, err = sf.moveOntoNewServer(t); err != nil {
		t.Fatal(err)
	}
	st, err = sf.svc.Status(ctx, member, projectID, stagingID, "qwen2-5-7b-instruct")
	if err != nil || st.State != StateWaitingForServer || st.Cost.Source != CostSourceServer || st.Cost.HourlyUSD != "1.5700" {
		t.Fatalf("moving: %+v, %v", st, err)
	}
	if st.MoveCost == nil || st.MoveCost.Source != CostSourceServer || st.MoveCost.HourlyUSD != "1.5700" || st.MoveCost.MonthlyUSD != "1146.10" {
		t.Errorf("new server's cost %+v", st.MoveCost)
	}
	sf.targets.targets[0].HourlyPriceKnown, sf.targets.targets[0].HourlyUSD = false, 0
	st, err = sf.svc.Status(ctx, member, projectID, stagingID, "qwen2-5-7b-instruct")
	if err != nil || st.MoveCost == nil || st.MoveCost.Source != CostSourceUnknown || st.MoveCost.HourlyUSD != "" || st.MoveCost.Known ||
		st.Cost.HourlyUSD != "1.5700" {
		t.Errorf("unknown new price: %+v, %v", st.MoveCost, err)
	}
}

// Ruling M42: a GPU server Loomplane created for a model is the model's,
// all of it, while nothing else holds a GPU there, even when the model
// needs one of its eight. The preview, the page, the month's cost and the
// budget notices all charge the whole server. Once another model holds a
// GPU there, each charges its share.
func TestCreatedServerChargesTheWholeServer(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	budget := &fakeBudget{}
	sf.svc.d.Prices, sf.svc.d.Budget = pricesOf{sf.targets}, budget
	// An eight-L40S size at $12.56 an hour.
	sf.targets.targets[0].Accelerator.Count, sf.targets.targets[0].Free, sf.targets.targets[0].HourlyUSD = 8, 8, 12.56
	sf.servers.hourly = 12.56
	plain, err := sf.svc.Preview(ctx, member, projectID, stagingID, DeployInput{EntryID: qwen7B, Target: onSize()})
	if err != nil || plain.GPU == nil || plain.GPU.Count != 1 || plain.Cost.HourlyUSD != "12.5600" || plain.Cost.Share != "8 of 8 GPUs" {
		t.Fatalf("preview: %+v, %v", plain, err)
	}
	pv, _ := sf.deployOnNewServer(t)
	if pv.Cost.HourlyUSD != "12.5600" {
		t.Fatalf("reviewed preview: %+v", pv.Cost)
	}
	sf.servers.succeed(pv.Server.ID)
	sf.gpusReady(provider.GPUStatusReady)
	host := &sf.targets.targets[1]
	host.Accelerator.Count, host.Free, host.HourlyUSD = 8, 8, 12.56
	if err = sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	cost := func(want, share string) {
		t.Helper()
		st, serr := sf.svc.Status(ctx, member, projectID, stagingID, "qwen2-5-7b-instruct")
		if serr != nil || st.Cost.HourlyUSD != want || st.Cost.Share != share || st.Cost.Source != CostSourceServer {
			t.Fatalf("status: %+v, %v", st, serr)
		}
	}
	// Planned, then running on one of the eight.
	cost("12.5600", "8 of 8 GPUs")
	r := current(t, sf.deploySetup).Resources["qwen2-5-7b-instruct"]
	sf.facts.runningOn(newConn, stagingID, "qwen2-5-7b-instruct", r.Kind, r.Settings, 1)
	host.Free = 7
	cost("12.5600", "8 of 8 GPUs")

	// The month's cost, and a budget only the whole server's price is on
	// track to pass.
	d := sf.row(t)
	d.MonthlyBudgetUSD = "2000.00"
	if err = sf.rows.Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	accrue := func(at time.Time) *Deployment {
		t.Helper()
		if aerr := sf.svc.Accrue(ctx, at); aerr != nil {
			t.Fatal(aerr)
		}
		return sf.row(t)
	}
	start := *sf.now
	accrue(start)
	if d = accrue(start.Add(time.Hour)); d.MonthCostUSD != "12.5600" {
		t.Fatalf("an hour on the whole server: %s", d.MonthCostUSD)
	}
	if sent := budget.notices(); len(sent) != 1 || sent[0].Kind != notifications.ModelBudgetProjected {
		t.Fatalf("notices %+v", sent)
	}

	// A move that stays on the server keeps all of it.
	same := MoveInput{Target: TargetChoice{Source: TargetConnection, ConnectionID: newConn, GPU: &provider.GPURequest{Kind: "nvidia-l40s", Count: 2}}}
	if mv, merr := sf.svc.MovePreview(ctx, member, projectID, stagingID, "qwen2-5-7b-instruct", same); merr != nil || mv.Cost.HourlyUSD != "12.5600" {
		t.Errorf("move on the server: %+v, %v", mv, merr)
	}

	// Another model takes a GPU there: each charges its share.
	host.Free = 6
	cost("1.5700", "1 of 8 GPUs")
	if mv, merr := sf.svc.MovePreview(ctx, member, projectID, stagingID, "qwen2-5-7b-instruct", same); merr != nil || mv.Cost.HourlyUSD != "3.1400" {
		t.Errorf("move on the shared server: %+v, %v", mv, merr)
	}
	if d = accrue(start.Add(2 * time.Hour)); d.MonthCostUSD != "14.1300" {
		t.Errorf("an hour sharing the server: %s", d.MonthCostUSD)
	}
}

// Review Focus 2: production's copy waits for its GPU server while
// staging's deploys onto a server you have. Staging's deploy adds the
// resource; once production's server is up, it adds production to it.
func TestServerUpAddsTheEnvironment(t *testing.T) {
	ctx := context.Background()
	sf := newServerFirst(t, cloudAdmin)
	pv, in := sf.reviewIn(t, productID)
	if _, err := sf.svc.Deploy(ctx, member, projectID, productID, in); err != nil {
		t.Fatal(err)
	}
	sf.targets.targets = append(sf.targets.targets, l40sServer())
	res, err := sf.svc.Deploy(ctx, member, projectID, stagingID, DeployInput{EntryID: qwen7B, Target: onGPU("nvidia-l40s")})
	if err != nil || res.Deployment.Resource != "qwen2-5-7b-instruct" {
		t.Fatalf("staging on gpu-box: %+v, %v", res, err)
	}
	edits := sf.recordEdits()

	sf.servers.succeed(pv.Server.ID)
	sf.gpusReady(provider.GPUStatusReady)
	sf.targets.targets = append(sf.targets.targets, l40sServer())
	if err = sf.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(edits.edits) != 1 || edits.edits[0].AddResource != nil || edits.edits[0].UpdateResource == nil {
		t.Fatalf("edits %+v", edits.edits)
	}
	u := edits.edits[0].UpdateResource
	if u.Resource != "qwen2-5-7b-instruct" || u.Environment != "production" || u.Enabled == nil || !*u.Enabled || u.Target == nil || *u.Target != newConn {
		t.Errorf("update %+v", u)
	}
	d, err := sf.rows.Get(ctx, "org", projectID, productID, "qwen2-5-7b-instruct")
	if err != nil || d.Phase != PhasePlanned || d.PlanID == "" || d.Message != "" {
		t.Errorf("production's row %+v, %v", d, err)
	}
	m := current(t, sf.deploySetup)
	if r := m.Resources["qwen2-5-7b-instruct"]; len(m.Resources) != 1 || !enabledIn(r, "production") || !enabledIn(r, "staging") ||
		r.Target != gpuConn || r.For("production", m.Defaults).Target != newConn {
		t.Errorf("resources %+v", m.Resources)
	}
}

// A stranded row counts as edited only when the manifest runs its
// resource in the row's environment: the resource running in staging
// alone is staging's copy, not production's.
func TestReconcileStrandedChecksTheEnvironment(t *testing.T) {
	ctx := context.Background()
	for _, serverFirst := range []bool{false, true} {
		sf := newServerFirst(t, cloudAdmin)
		if _, _, err := manifests.NewService(sf.revisions, memProjects{}, aiAdmin, nil).Edit(ctx, member, projectID, manifests.EditInput{
			AddResource: &manifests.NewResource{Name: "llm", Kind: "model-tei", Provider: "server", Target: cpuConn, Version: "1.9", EnabledIn: "staging",
				Settings: map[string]string{"entry": bgeSmall}},
		}); err != nil {
			t.Fatal(err)
		}
		d := &Deployment{OrgID: "org", ProjectID: projectID, EnvironmentID: productID, Resource: "llm", EntryID: bgeSmall,
			Quantization: "fp32", Engine: "tei", Phase: PhasePlanned, RequestedBy: member}
		if serverFirst {
			d.ServerCloudID, d.ServerOperationID, d.ServerName = cloudID, "infraop_x", "shop-llm-production-gpu"
		}
		if err := sf.rows.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
		sf.advance(StrandedAfter + time.Minute)
		if err := sf.svc.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		got, err := sf.rows.Get(ctx, "org", projectID, productID, "llm")
		switch {
		case !serverFirst && !errors.Is(err, ErrNotFound):
			t.Errorf("a deploy that never reached production's block stays: %+v, %v", got, err)
		case serverFirst && (err != nil || got.Phase != PhaseWaitingForServer):
			t.Errorf("a server-first row: %+v, %v", got, err)
		}
	}
}

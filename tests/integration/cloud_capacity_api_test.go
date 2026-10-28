//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/pkg/provider"
)

// capacityAPI is one org with a Cloud, driven over HTTP, plus the database
// handles the tests use to seed rows the API only reads.
type capacityAPI struct {
	r                   *running
	c                   *client
	org, cloud, project string
	sql                 *pgx.Conn
	store               *cloudinfra.PgStore
	deploys             *deployments.PgStore
}

func capacityAPISetup(t *testing.T, configure ...func(*config.Config)) *capacityAPI {
	t.Helper()
	cfg, r, c, org, project := cloudAPISetup(t, configure...)
	t.Cleanup(r.stop)
	var cloud cloudprofiles.Profile
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+project+"/clouds", cloudprofiles.CreateInput{Name: "Capacity"}, &cloud)
	db := pgdriver.New()
	if err := db.Open(t.Context(), cfg.Database.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sql, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sql.Close(t.Context()) })
	now := time.Now().UTC()
	if err = connections.NewPgStore(db).CreateConnection(t.Context(), &connections.Connection{ID: "capacity-do", OrgID: org, Cloud: "digitalocean", Name: "Capacity", Account: "account", Status: "ready", Generation: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return &capacityAPI{r: r, c: c, org: org, cloud: cloud.ID, project: project, sql: sql, store: cloudinfra.NewPgStore(db), deploys: deployments.NewPgStore(db)}
}

func (a *capacityAPI) path(suffix string) string { return "/api/v1/clouds/" + a.cloud + suffix }

// capacityPolicyView is what the capacity-policy route answers: the policy
// and whether this server adds servers on its own.
type capacityPolicyView struct {
	CloudID            string                    `json:"cloud_id"`
	Policy             cloudinfra.CapacityPolicy `json:"policy"`
	AutomaticExpansion *bool                     `json:"automatic_expansion"`
}

// policy reads the Cloud's saved policy through the API.
func (a *capacityAPI) policy(t *testing.T) cloudinfra.CapacityPolicy {
	t.Helper()
	var v capacityPolicyView
	a.c.must(http.StatusOK, http.MethodGet, a.path("/capacity-policy"), nil, &v)
	return v.Policy
}

func (a *capacityAPI) pool(t *testing.T, id, kind string) {
	t.Helper()
	if err := a.store.SavePool(t.Context(), &cloudinfra.Pool{ID: id, OrgID: a.org, CloudID: a.cloud, Name: id, Revision: 1, Members: []cloudinfra.PoolMember{}, AcceleratorKind: kind}, 0); err != nil {
		t.Fatal(err)
	}
}

// expansion inserts the row the expansion worker would, without a provider.
func (a *capacityAPI) expansion(t *testing.T, e cloudinfra.Expansion, created time.Time) {
	t.Helper()
	e.OrgID, e.CloudID, e.ConnectionID, e.ConnectionGeneration = a.org, a.cloud, "capacity-do", 1
	e.Provider, e.AccountID, e.Region, e.Architecture, e.Image = "digitalocean", "account", "tor1", "amd64", "ubuntu"
	e.CreatedAt, e.UpdatedAt = created, created
	if e.IntentHash == "" {
		e.IntentHash = "intent-" + e.ID
	}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.sql.Exec(t.Context(), `INSERT INTO loomplane_cloud_expansions(org_id,cloud_id,id,pool_id,status,intent_hash,policy_revision,servers,cpu_millis,memory_bytes,storage_bytes,provider,account_id,connection_id,connection_generation,region,size,architecture,image,body,revision,gpu_kind,gpus,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$24)`,
		e.OrgID, e.CloudID, e.ID, e.PoolID, e.Status, e.IntentHash, e.PolicyRevision, e.Resources.Servers, e.Resources.CPUMillis, e.Resources.MemoryBytes, e.Resources.StorageBytes, e.Provider, e.AccountID, e.ConnectionID, e.ConnectionGeneration, e.Region, e.Size, e.Architecture, e.Image, raw, e.Revision, e.Resources.GPUKind, e.Resources.GPUs, created); err != nil {
		t.Fatal(err)
	}
}

// capacityPost returns a POST's status and body, whatever the status.
func capacityPost(t *testing.T, c *client, path string, body any) (int, string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, c.base+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Publishable-Key", c.key)
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(out)
}

// rivalCloud signs up another organization with a Cloud of its own and
// returns that Cloud's ID.
func (a *capacityAPI) rivalCloud(t *testing.T) string {
	t.Helper()
	other := &client{t: t, base: a.c.base, key: a.c.key}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	other.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "rival@example.test", "password": "Correct-horse-battery-9"}, &auth)
	other.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	other.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Rival", "slug": "rival"}, &org)
	other.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var project struct {
		ID string `json:"id"`
	}
	other.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Rival app", "repository_url": "https://github.com/acme/mono"}, &project)
	var rival cloudprofiles.Profile
	other.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+project.ID+"/clouds", cloudprofiles.CreateInput{Name: "Rival"}, &rival)
	return rival.ID
}

// readOnly is a client holding a read-only key for a's organization.
func (a *capacityAPI) readOnly(t *testing.T) *client {
	t.Helper()
	var read keyBody
	a.c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "viewer", "access": "read", "expires_in_days": 7}, &read)
	return &client{t: t, base: a.c.base, key: a.c.key, token: read.Secret}
}

func capacityLimits() cloudinfra.ResourceVector {
	return cloudinfra.ResourceVector{Servers: 2, CPUMillis: 4000, MemoryBytes: 4 << 30, StorageBytes: 80_000_000_000}
}

func TestCapacityAPIStaleRevisions(t *testing.T) {
	a := capacityAPISetup(t)
	policy := a.policy(t)
	if policy.Revision != 0 || policy.Enabled {
		t.Fatalf("unconfigured policy %+v", policy)
	}
	in := cloudinfra.CapacityPolicyInput{ExpectedRevision: 0, Policy: cloudinfra.CapacityPolicy{RequireExpansionApproval: true, Limits: capacityLimits()}}
	var saved capacityPolicyView
	a.c.must(http.StatusOK, http.MethodPost, a.path("/capacity-policy"), in, &saved)
	policy = saved.Policy
	if policy.Revision != 1 || !policy.RequireExpansionApproval || policy.CloudID != a.cloud || saved.CloudID != a.cloud {
		t.Fatalf("saved policy %+v", saved)
	}
	// The second editor opened revision 0 too.
	a.c.must(http.StatusConflict, http.MethodPost, a.path("/capacity-policy"), in, nil)
	policy = a.policy(t)
	if policy.Revision != 1 {
		t.Fatalf("stale save changed the policy %+v", policy)
	}

	a.pool(t, "grow", "")
	a.expansion(t, cloudinfra.Expansion{ID: "exp-stale", PoolID: "grow", Status: "awaiting_approval", PolicyRevision: 1, Revision: 2, Size: "small", Resources: cloudinfra.ResourceVector{Servers: 1, CPUMillis: 2000, MemoryBytes: 2 << 30, StorageBytes: 20_000_000_000}}, time.Now().UTC())
	a.c.must(http.StatusConflict, http.MethodPost, a.path("/expansions/exp-stale/approve"), map[string]int64{"expected_revision": 1}, nil)
	a.c.must(http.StatusNotFound, http.MethodPost, a.path("/expansions/missing/approve"), map[string]int64{"expected_revision": 1}, nil)
	var list struct {
		Expansions []cloudinfra.Expansion `json:"expansions"`
	}
	a.c.must(http.StatusOK, http.MethodGet, a.path("/expansions"), nil, &list)
	if len(list.Expansions) != 1 || list.Expansions[0].Status != "awaiting_approval" || list.Expansions[0].Revision != 2 || list.Expansions[0].ApprovedBy != "" {
		t.Fatalf("stale approval changed the expansion %+v", list.Expansions)
	}
}

// GET capacity-policy says whether this server adds servers on its own. The
// flag is read-only: a save can't set it, and it never lands in the saved body.
func TestCapacityAPIAutomaticExpansionFlag(t *testing.T) {
	a := capacityAPISetup(t)
	var got capacityPolicyView
	a.c.must(http.StatusOK, http.MethodGet, a.path("/capacity-policy"), nil, &got)
	if got.AutomaticExpansion == nil || *got.AutomaticExpansion || got.CloudID != a.cloud {
		t.Fatalf("default flag %+v", got)
	}
	policy := map[string]any{"limits": capacityLimits(), "automatic_expansion": true}
	a.c.must(http.StatusOK, http.MethodPost, a.path("/capacity-policy"), map[string]any{"expected_revision": 0, "automatic_expansion": true, "policy": policy}, nil)
	a.c.must(http.StatusOK, http.MethodGet, a.path("/capacity-policy"), nil, &got)
	if got.Policy.Revision != 1 || got.AutomaticExpansion == nil || *got.AutomaticExpansion {
		t.Fatalf("a save turned the flag on %+v", got)
	}
	var stored bool
	if err := a.sql.QueryRow(t.Context(), `SELECT body ? 'automatic_expansion' FROM loomplane_cloud_capacity_policies WHERE org_id=$1 AND cloud_id=$2`, a.org, a.cloud).Scan(&stored); err != nil || stored {
		t.Fatalf("saved body holds the flag: %v %v", stored, err)
	}
}

func TestCapacityAPIStatuses(t *testing.T) {
	a := capacityAPISetup(t)
	// Enabling without a server ceiling names the missing ceiling.
	limits := capacityLimits()
	limits.Servers = 0
	enable := cloudinfra.CapacityPolicyInput{Policy: cloudinfra.CapacityPolicy{Enabled: true, Limits: limits}}
	status, body := capacityPost(t, a.c, a.path("/capacity-policy"), enable)
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "server ceiling") {
		t.Fatalf("enable without servers: %d %s", status, body)
	}
	a.c.must(http.StatusUnprocessableEntity, http.MethodPost, a.path("/capacity-policy"), map[string]any{"expected_revision": "one"}, nil)

	// A read-only key reads nothing it can change.
	viewer := a.readOnly(t)
	viewer.must(http.StatusForbidden, http.MethodPost, a.path("/capacity-policy"), cloudinfra.CapacityPolicyInput{Policy: cloudinfra.CapacityPolicy{Limits: capacityLimits()}}, nil)
	viewer.must(http.StatusForbidden, http.MethodPost, a.path("/capacity-usage/refresh"), nil, nil)
	viewer.must(http.StatusForbidden, http.MethodPost, a.path("/expansions/any/approve"), map[string]int64{"expected_revision": 1}, nil)

	// Another organization's Cloud doesn't exist from here.
	rival := a.rivalCloud(t)
	for _, suffix := range []string{"/capacity-policy", "/capacity-usage", "/expansions"} {
		a.c.must(http.StatusNotFound, http.MethodGet, "/api/v1/clouds/"+rival+suffix, nil, nil)
	}

	// Usage always carries both lists, even with nothing counted.
	var usage map[string]json.RawMessage
	a.c.must(http.StatusOK, http.MethodGet, a.path("/capacity-usage"), nil, &usage)
	for _, key := range []string{"gpus", "frontends"} {
		if !strings.HasPrefix(string(usage[key]), "[") {
			t.Fatalf("usage %s is %s", key, usage[key])
		}
	}
}

func TestCapacityAPIGPUFields(t *testing.T) {
	a := capacityAPISetup(t)
	a.pool(t, "l4", "nvidia-l4")
	in := cloudinfra.CapacityPolicyInput{Policy: cloudinfra.CapacityPolicy{Limits: capacityLimits(), GPULimits: []cloudinfra.GPULimit{{Kind: "nvidia-l4", Maximum: 2}}, Pools: []cloudinfra.PoolExpansionPolicy{{PoolID: "l4", AcceleratorKind: "nvidia-l4", MaxServers: 1}}}}
	a.c.must(http.StatusOK, http.MethodPost, a.path("/capacity-policy"), in, nil)
	var view struct {
		Policy struct {
			GPULimits []cloudinfra.GPULimit `json:"gpu_limits"`
			Pools     []struct {
				PoolID          string `json:"pool_id"`
				AcceleratorKind string `json:"accelerator_kind"`
			} `json:"pools"`
		} `json:"policy"`
	}
	a.c.must(http.StatusOK, http.MethodGet, a.path("/capacity-policy"), nil, &view)
	policy := view.Policy
	if len(policy.GPULimits) != 1 || policy.GPULimits[0] != (cloudinfra.GPULimit{Kind: "nvidia-l4", Maximum: 2}) || len(policy.Pools) != 1 || policy.Pools[0].AcceleratorKind != "nvidia-l4" {
		t.Fatalf("GPU policy %+v", policy)
	}
	var usage cloudinfra.CapacityUsage
	a.c.must(http.StatusOK, http.MethodGet, a.path("/capacity-usage"), nil, &usage)
	if len(usage.GPUs) != 1 || usage.GPUs[0].Kind != "nvidia-l4" || usage.GPUs[0].Limit == nil || *usage.GPUs[0].Limit != 2 {
		t.Fatalf("GPU usage %+v", usage.GPUs)
	}

	a.pool(t, "h100", "nvidia-h100-80gb")
	now := time.Now().UTC()
	a.expansion(t, cloudinfra.Expansion{ID: "exp-cpu", PoolID: "l4", Status: "blocked", PolicyRevision: 1, Revision: 1, Size: "small", Resources: cloudinfra.ResourceVector{Servers: 1, CPUMillis: 2000, MemoryBytes: 2 << 30, StorageBytes: 20_000_000_000}}, now.Add(-time.Hour))
	a.expansion(t, cloudinfra.Expansion{ID: "exp-cancelled", PoolID: "l4", Status: "cancelled", PolicyRevision: 1, Revision: 1, Size: "small", Resources: cloudinfra.ResourceVector{Servers: 1}}, now.Add(time.Hour))
	a.expansion(t, cloudinfra.Expansion{ID: "exp-gpu", PoolID: "h100", Status: "awaiting_approval", PolicyRevision: 1, Revision: 1, Size: "gpu-h100x1-80gb",
		Resources:          cloudinfra.ResourceVector{Servers: 1, CPUMillis: 20000, MemoryBytes: 240 << 30, StorageBytes: 720_000_000_000, GPUKind: "nvidia-h100-80gb", GPUs: 1},
		EstimatedHourlyUSD: 3.39, HourlyPriceKnown: true, Accelerator: &provider.Accelerator{Kind: "nvidia-h100-80gb", Vendor: provider.VendorNVIDIA, Count: 1, MemoryGiB: 80},
		Template: cloudinfra.PoolExpansionPolicy{PoolID: "h100", AcceleratorKind: "nvidia-h100-80gb"}}, now)
	var list struct {
		Expansions []struct {
			ID                 string  `json:"id"`
			EstimatedHourlyUSD float64 `json:"estimated_hourly_usd"`
			HourlyPriceKnown   bool    `json:"hourly_price_known"`
			Accelerator        *struct {
				Kind string `json:"kind"`
			} `json:"accelerator"`
			Resources struct {
				GPUKind string `json:"gpu_kind"`
				GPUs    int64  `json:"gpus"`
			} `json:"resources"`
		} `json:"expansions"`
	}
	a.c.must(http.StatusOK, http.MethodGet, a.path("/expansions"), nil, &list)
	// Newest first, and a cancelled expansion isn't listed.
	if len(list.Expansions) != 2 || list.Expansions[0].ID != "exp-gpu" || list.Expansions[1].ID != "exp-cpu" {
		t.Fatalf("expansions %+v", list.Expansions)
	}
	gpu := list.Expansions[0]
	if gpu.EstimatedHourlyUSD != 3.39 || !gpu.HourlyPriceKnown || gpu.Accelerator == nil || gpu.Accelerator.Kind != "nvidia-h100-80gb" || gpu.Resources.GPUKind != "nvidia-h100-80gb" || gpu.Resources.GPUs != 1 {
		t.Fatalf("GPU expansion %+v", gpu)
	}
	if cpu := list.Expansions[1]; cpu.HourlyPriceKnown || cpu.Accelerator != nil {
		t.Fatalf("an unpriced expansion claims a price %+v", cpu)
	}
}

// pausedDeployment seeds a deployment the capacity check paused, the way the
// workflow would, and returns it at wait revision 1.
func (a *capacityAPI) pausedDeployment(t *testing.T, org, id string) *deployments.Deployment {
	t.Helper()
	var envs struct {
		Environments []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"environments"`
	}
	a.c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+a.project+"/environments", nil, &envs)
	env := ""
	for _, e := range envs.Environments {
		if e.Name == "production" {
			env = e.ID
		}
	}
	if _, err := a.sql.Exec(t.Context(), `INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) SELECT 'manifest-'||$2,$1,$3,coalesce(max(revision),0)+1,'version: 1','digest','usr',now() FROM loomplane_manifests WHERE project_id=$3`, org, id, a.project); err != nil {
		t.Fatal(err)
	}
	if _, err := a.sql.Exec(t.Context(), `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,manifest_id,manifest_revision,created_by,created_at) VALUES('plan-'||$2,$1,$3,$4,'production','approved','manifest-'||$2,1,'usr',now())`, org, id, a.project, env); err != nil {
		t.Fatal(err)
	}
	_, err := a.deploys.CreateDeployment(t.Context(), &deployments.Deployment{ID: id, OrgID: org, ProjectID: a.project, EnvironmentID: env, Environment: "production", PlanID: "plan-" + id, Status: deployments.StatusRunning, StartedAt: time.Now(), StartedBy: "usr", Steps: []deployments.Step{{ActionID: "service:api", Status: deployments.StepPending}}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := a.deploys.SetCapacityWait(t.Context(), org, id, 0, []cloudinfra.CapacityBlock{{Reason: "hard_limit", Resource: "gpus:nvidia-l4", CloudID: a.cloud, Limit: 2, Counted: 2, Requested: 1, PolicyRevision: 1}}, time.Now())
	if err != nil || d.Status != deployments.StatusPaused || d.CapacityWait.Revision != 1 {
		t.Fatalf("seed paused deployment: %+v %v", d, err)
	}
	return d
}

func (a *capacityAPI) capacityEvents(t *testing.T, id string, revision int64) int {
	t.Helper()
	var n int
	if err := a.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_deployment_capacity_events WHERE org_id=$1 AND deployment_id=$2 AND revision=$3`, a.org, id, revision).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCapacityAPIWaits(t *testing.T) {
	a := capacityAPISetup(t)
	a.pausedDeployment(t, a.org, "dep-wait")
	base := "/api/v1/deployments/dep-wait"
	wait := func(rev int64) map[string]int64 { return map[string]int64{"expected_revision": rev} }

	var d deployments.Deployment
	a.c.must(http.StatusOK, http.MethodGet, base, nil, &d)
	if d.Status != deployments.StatusPaused || d.CapacityWait == nil || d.CapacityWait.Revision != 1 || len(d.CapacityWait.Blocks) != 1 || d.CapacityWait.Blocks[0].Resource != "gpus:nvidia-l4" {
		t.Fatalf("paused deployment %+v", d)
	}

	// A wait that moved since it was read answers 409 and changes nothing.
	status, body := capacityPost(t, a.c, base+"/capacity/resume", wait(0))
	if status != http.StatusConflict || !strings.Contains(body, "capacity wait changed") {
		t.Fatalf("stale resume: %d %s", status, body)
	}
	a.c.must(http.StatusBadRequest, http.MethodPost, base+"/capacity/resume", map[string]any{"expected_revision": "one"}, nil)

	// A viewer reads the wait but can't resume or cancel it.
	var invite struct {
		AcceptPath string `json:"accept_path"`
	}
	a.c.must(http.StatusCreated, http.MethodPost, "/api/v1/invitations", map[string]string{"email": "watcher@example.test", "role": "viewer"}, &invite)
	accept, err := url.Parse(invite.AcceptPath)
	if err != nil {
		t.Fatal(err)
	}
	viewer := &client{t: t, base: a.c.base, key: a.c.key}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	viewer.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "watcher@example.test", "password": "Correct-horse-battery-9"}, &auth)
	viewer.token = auth.SessionToken
	if code := viewer.do(http.MethodPost, "/authsome/v1/orgs/invitations/accept", map[string]string{"token": accept.Query().Get("token")}, nil); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("accept: %d", code)
	}
	viewer.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": a.org}, nil)
	viewer.must(http.StatusOK, http.MethodGet, base, nil, nil)
	viewer.must(http.StatusForbidden, http.MethodPost, base+"/capacity/resume", wait(1), nil)
	viewer.must(http.StatusForbidden, http.MethodPost, base+"/capacity/cancel", wait(1), nil)

	// Another organization's deployment doesn't exist from here.
	a.pausedDeployment(t, "other-org", "dep-elsewhere")
	a.c.must(http.StatusNotFound, http.MethodPost, "/api/v1/deployments/dep-elsewhere/capacity/resume", wait(1), nil)
	a.c.must(http.StatusNotFound, http.MethodPost, "/api/v1/deployments/dep-elsewhere/capacity/cancel", wait(1), nil)
	a.c.must(http.StatusNotFound, http.MethodPost, "/api/v1/deployments/missing/capacity/resume", wait(1), nil)
	a.c.must(http.StatusOK, http.MethodGet, base, nil, &d)
	if d.Status != deployments.StatusPaused || d.CapacityWait.Revision != 1 || a.capacityEvents(t, "dep-wait", 2) != 0 {
		t.Fatalf("a refused resume changed the wait %+v", d)
	}

	// Resuming at the current revision queues one durable wake for the workflow.
	// Decode each answer fresh: an omitted capacity_wait mustn't keep the last one.
	d = deployments.Deployment{}
	a.c.must(http.StatusOK, http.MethodPost, base+"/capacity/resume", wait(1), &d)
	if d.Status != deployments.StatusRunning || d.CapacityWait != nil || d.FinishedAt != nil {
		t.Fatalf("resumed %+v", d)
	}
	if n := a.capacityEvents(t, "dep-wait", 2); n != 1 {
		t.Fatalf("resume wrote %d capacity events", n)
	}

	// A running deployment has no wait to cancel.
	status, body = capacityPost(t, a.c, base+"/capacity/cancel", wait(2))
	if status != http.StatusConflict {
		t.Fatalf("cancel while running: %d %s", status, body)
	}

	// Paused again, it cancels, and a cancelled deployment can't resume.
	if _, err = a.deploys.SetCapacityWait(t.Context(), a.org, "dep-wait", 2, []cloudinfra.CapacityBlock{{Reason: "hard_limit", Resource: "servers", CloudID: a.cloud, Limit: 2, Counted: 2, Requested: 1}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	d = deployments.Deployment{}
	a.c.must(http.StatusOK, http.MethodPost, base+"/capacity/cancel", wait(3), &d)
	if d.Status != deployments.StatusCancelled || d.CapacityWait != nil || d.FinishedAt == nil {
		t.Fatalf("cancelled %+v", d)
	}
	a.c.must(http.StatusConflict, http.MethodPost, base+"/capacity/resume", wait(4), nil)
}

// scalingPoolAPI is capacityAPI with a DigitalOcean account that sells one L4
// GPU Droplet size, so an enabled policy can grow a GPU pool from zero.
type scalingPoolAPI struct {
	*capacityAPI
	connection string
}

func scalingPoolSetup(t *testing.T) *scalingPoolAPI {
	t.Helper()
	const token = "dop_v1_0123456789abcdef"
	do := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v2/account":
			_, _ = w.Write([]byte(`{"account":{"uuid":"acct-gpu","email":"ada@example.com","status":"active"}}`))
		case "/v2/regions":
			_, _ = w.Write([]byte(`{"regions":[{"slug":"tor1","name":"Toronto 1","available":true}],"links":{}}`))
		case "/v2/sizes":
			_, _ = w.Write([]byte(`{"sizes":[{"slug":"gpu-l4x1-24gb","memory":16384,"vcpus":4,"disk":100,"available":true,"price_hourly":0.85,"price_monthly":632.4,"regions":["tor1"],"gpu_info":{"count":1,"model":"nvidia_l4","vram":{"amount":24,"unit":"gib"}}}],"links":{}}`))
		case "/v2/images":
			_, _ = w.Write([]byte(`{"images":[{"id":1,"slug":"gpu-h100x1-base","name":"AI/ML Ready","distribution":"Ubuntu","public":true,"regions":["tor1"],"min_disk_size":50}],"links":{}}`))
		default:
			t.Errorf("asked DigitalOcean %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(do.Close)
	a := capacityAPISetup(t, func(cfg *config.Config) { cfg.Clouds.DigitalOceanEndpoint = do.URL })
	var conn struct {
		ID string `json:"id"`
	}
	a.c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]string{"cloud": "digitalocean", "name": "GPU", "credential": token}, &conn)
	return &scalingPoolAPI{capacityAPI: a, connection: conn.ID}
}

// input grows an L4 pool from zero to two servers under a fresh policy.
func (a *scalingPoolAPI) input() cloudinfra.ScalingPoolInput {
	return cloudinfra.ScalingPoolInput{Name: "Inference", AcceleratorKind: "nvidia-l4",
		Policy:   cloudinfra.CapacityPolicy{Enabled: true, Limits: capacityLimits(), GPULimits: []cloudinfra.GPULimit{{Kind: "nvidia-l4", Maximum: 2}}},
		Template: cloudinfra.PoolExpansionPolicy{AcceleratorKind: "nvidia-l4", ConnectionID: a.connection, Region: "tor1", Architecture: "amd64", Image: "gpu-h100x1-base", AllowedSizes: []string{"gpu-l4x1-24gb"}, MaxServers: 2}}
}

func (a *scalingPoolAPI) poolRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := a.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_server_pools WHERE org_id=$1 AND cloud_id=$2`, a.org, a.cloud).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestScalingPoolCreatesPoolAndPolicyTogether(t *testing.T) {
	a := scalingPoolSetup(t)
	var created cloudinfra.ScalingPool
	a.c.must(http.StatusOK, http.MethodPost, a.path("/scaling-pools"), a.input(), &created)
	pool, policy := created.Pool, created.Policy
	if pool == nil || pool.ID == "" || pool.Name != "Inference" || len(pool.Members) != 0 || pool.AcceleratorKind != "nvidia-l4" || pool.Revision != 1 {
		t.Fatalf("pool %+v", pool)
	}
	if policy == nil || policy.Revision != 1 || !policy.Enabled || len(policy.Pools) != 1 || policy.Pools[0].PoolID != pool.ID || policy.Pools[0].AcceleratorKind != "nvidia-l4" || policy.Pools[0].BootstrapSize != "gpu-l4x1-24gb" {
		t.Fatalf("policy %+v", policy)
	}

	// Both are saved: the pool lists with no servers, and the policy names it.
	var pools struct {
		Pools []cloudinfra.Pool `json:"pools"`
	}
	a.c.must(http.StatusOK, http.MethodGet, a.path("/pools"), nil, &pools)
	if len(pools.Pools) != 1 || pools.Pools[0].ID != pool.ID || len(pools.Pools[0].Members) != 0 || pools.Pools[0].AcceleratorKind != "nvidia-l4" {
		t.Fatalf("pools %+v", pools.Pools)
	}
	saved := a.policy(t)
	if saved.Revision != 1 || len(saved.Pools) != 1 || saved.Pools[0].PoolID != pool.ID {
		t.Fatalf("saved policy %+v", saved)
	}

	// A second pool joins the policy beside the first.
	in := a.input()
	in.Name, in.ExpectedRevision, in.Policy = "Batch", 1, saved
	var second cloudinfra.ScalingPool
	a.c.must(http.StatusOK, http.MethodPost, a.path("/scaling-pools"), in, &second)
	if second.Pool.ID == pool.ID || second.Policy.Revision != 2 || len(second.Policy.Pools) != 2 || second.Policy.Pools[0].PoolID != pool.ID || second.Policy.Pools[1].PoolID != second.Pool.ID || a.poolRows(t) != 2 {
		t.Fatalf("second pool %+v", second.Policy)
	}
}

func TestScalingPoolRollsBackOnPolicyError(t *testing.T) {
	a := scalingPoolSetup(t)
	// Refused before the transaction: the GPU pool has no Cloud GPU ceiling.
	in := a.input()
	in.Policy.GPULimits = nil
	status, body := capacityPost(t, a.c, a.path("/scaling-pools"), in)
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "A GPU pool needs a GPU ceiling for nvidia-l4.") {
		t.Fatalf("no GPU ceiling: %d %s", status, body)
	}
	// A GPU ceiling on the pool alone would block it, so it is refused by name.
	in = a.input()
	in.Template.Limits = &cloudinfra.ResourceVector{GPUKind: "nvidia-l4", GPUs: 1}
	status, body = capacityPost(t, a.c, a.path("/scaling-pools"), in)
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "A pool GPU ceiling needs the pool's server ceiling too.") {
		t.Fatalf("GPU-only pool ceiling: %d %s", status, body)
	}
	// Refused inside the transaction, after the pool row is written: the
	// frontend limit names a target this Cloud doesn't have.
	in = a.input()
	in.Policy.FrontendLimits = []cloudinfra.FrontendLimit{{TargetID: "missing"}}
	status, body = capacityPost(t, a.c, a.path("/scaling-pools"), in)
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "The frontend limit for target missing names no managed target on this Cloud.") {
		t.Fatalf("missing frontend target: %d %s", status, body)
	}
	if n := a.poolRows(t); n != 0 {
		t.Fatalf("a refused create left %d pool rows", n)
	}
	policy := a.policy(t)
	if policy.Revision != 0 || len(policy.Pools) != 0 {
		t.Fatalf("a refused create saved the policy %+v", policy)
	}
}

func TestScalingPoolStaleRevision(t *testing.T) {
	a := scalingPoolSetup(t)
	a.c.must(http.StatusOK, http.MethodPost, a.path("/capacity-policy"), cloudinfra.CapacityPolicyInput{Policy: cloudinfra.CapacityPolicy{Limits: capacityLimits()}}, nil)
	// The editor opened revision 0, before that save.
	a.c.must(http.StatusConflict, http.MethodPost, a.path("/scaling-pools"), a.input(), nil)
	if n := a.poolRows(t); n != 0 {
		t.Fatalf("a stale create left %d pool rows", n)
	}
	policy := a.policy(t)
	if policy.Revision != 1 || policy.Enabled || len(policy.Pools) != 0 {
		t.Fatalf("a stale create changed the policy %+v", policy)
	}
}

func TestScalingPoolScope(t *testing.T) {
	a := scalingPoolSetup(t)
	a.readOnly(t).must(http.StatusForbidden, http.MethodPost, a.path("/scaling-pools"), a.input(), nil)
	rival := a.rivalCloud(t)
	a.c.must(http.StatusNotFound, http.MethodPost, "/api/v1/clouds/"+rival+"/scaling-pools", a.input(), nil)
	if n := a.poolRows(t); n != 0 {
		t.Fatalf("a refused create left %d pool rows", n)
	}
}

//go:build integration && acceptance

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/catalogproxy"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/internal/recipes"
	mf "github.com/xraph/loomplane/pkg/manifest"
)

// acceptanceKind is the composed kind this run deploys. It's registered
// only in this test binary, built with the acceptance tag, never in the
// product's catalog.
const acceptanceKind = "lpaccept"

// acceptanceAdminHeader carries the kind's generated admin token to its
// control port, as the fixture image expects.
const acceptanceAdminHeader = "X-Lpaccept-Admin-Token"

var registerAcceptanceAdmin sync.Once

// acceptanceStatus is what the fixture's control port reports.
type acceptanceStatus struct {
	Component     string    `json:"component"`
	MigrateRuns   int       `json:"migrate_runs"`
	MigratedAt    time.Time `json:"migrated_at"`
	StartedAt     time.Time `json:"started_at"`
	ReadyRefused  int64     `json:"ready_refused"`
	ReadyAnswered int64     `json:"ready_answered"`
	AdminToken    bool      `json:"admin_token"`
}

// TestAcceptanceCatalogComposed deploys a composed catalog kind, with a
// provisioned Postgres, a migration job and a process with HTTP readiness
// and a control port, to the run's real SSH/Docker server and kind
// cluster, then removes it through the product.
func TestAcceptanceCatalogComposed(t *testing.T) {
	path := os.Getenv("LOOMPLANE_INFRASTRUCTURE_STATE")
	if path == "" {
		t.Skip("run make acceptance-catalog to create the isolated real targets")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state infrastructureState
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(state.RunID, "loomplane-e2e-") || state.Namespace != state.RunID {
		t.Fatal("invalid acceptance ownership record")
	}
	image := acceptanceImage(t, state)
	registerAcceptanceKind(t, image)
	credential, settings := fixtureSSH(t, state)
	kubeconfig, err := os.ReadFile(state.Kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	kubectl := []string{"kubectl", "--kubeconfig", state.Kubeconfig, "--context", "kind-" + state.Cluster}
	storageClass := retainedStorageClass(t, state, kubectl)
	architecture := fixtureCommand(t, "docker", "exec", state.Builder, "docker", "version", "--format", "{{.Server.Arch}}")

	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "catalog-acceptance@example.test", "password": "Acceptance-Catalog-42"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Catalog acceptance", "slug": "catalog-acceptance"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	t.Cleanup(func() {
		// Only what this organization created is eligible, even when the
		// product's own removal didn't run.
		ids := fixtureCommand(t, "docker", "exec", state.Builder, "docker", "ps", "-aq", "--filter", "label="+owned.LabelOrg+"="+org.ID)
		if ids != "" {
			fixtureCommand(t, append([]string{"docker", "exec", state.Builder, "docker", "rm", "-f"}, strings.Fields(ids)...)...)
		}
		fixtureCommand(t, append(slices.Clone(kubectl), "-n", state.Namespace, "delete", "deployment,statefulset,job,service,secret,configmap,networkpolicy,serviceaccount,role,rolebinding,pvc", "--selector="+owned.LabelOrg+"="+org.ID, "--ignore-not-found", "--wait=false")...)
	})
	access, err := forge.InjectType[*deployments.CatalogAccess](r.app.(forge.App).Container())
	if err != nil {
		t.Fatal(err)
	}

	type connection struct {
		ID      string            `json:"id"`
		Status  string            `json:"status"`
		Details map[string]string `json:"details"`
		Missing []string          `json:"missing"`
	}
	var serverConn, clusterConn connection
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "server", "name": "Catalog acceptance Docker", "credential": credential, "settings": settings}, &serverConn)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "kubernetes", "name": "Catalog acceptance cluster", "credential": string(kubeconfig), "settings": map[string]string{
		"context": "kind-" + state.Cluster, "namespace": state.Namespace, "architecture": architecture, "storage_class": storageClass,
	}}, &clusterConn)
	if serverConn.Status != "ready" || clusterConn.Status != "ready" {
		t.Fatalf("real connections are not ready: server %+v cluster %+v", serverConn, clusterConn)
	}
	if clusterConn.Details["persistent_storage"] != "ready" || clusterConn.Details["postgres_recovery_permissions"] != "ready" || clusterConn.Details["service_proxy"] != "ready" {
		t.Fatalf("cluster prerequisites: %+v", clusterConn)
	}
	t.Logf("connections ready: server %s (architecture %s), cluster %s (storage class %s)", serverConn.ID, serverConn.Details["architecture"], clusterConn.ID, storageClass)

	// Each target is its own subtest, cluster first, so one target failing
	// doesn't hide the other's result.
	targets := []struct{ provider, connection string }{
		{"kubernetes", clusterConn.ID},
		{"server", serverConn.ID},
	}
	for _, target := range targets {
		t.Run(target.provider, func(t *testing.T) {
			run := composedRun{t: t, c: &client{t: t, base: c.base, token: c.token, key: c.key}, state: state, kubectl: kubectl, orgID: org.ID, provider: target.provider, connection: target.connection, access: access, base: r.srv.URL}
			run.roundTrip()
		})
	}
}

// composedRun deploys and removes the acceptance kind on one target.
type composedRun struct {
	t          *testing.T
	c          *client
	state      infrastructureState
	kubectl    []string
	orgID      string
	provider   string
	connection string
	access     *deployments.CatalogAccess
	base       string
	projectID  string
}

func (x *composedRun) roundTrip() {
	t, c := x.t, x.c
	var project struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Composed catalog on " + x.provider}, &project)
	x.projectID = project.ID
	waitReady(t, c, project.ID)
	yaml := fmt.Sprintf("version: 1\nresources:\n  app: {kind: %s, provider: %s, target: %s}\n", acceptanceKind, x.provider, x.connection)
	if _, err := mf.Parse([]byte(yaml)); err != nil {
		t.Fatal(err)
	}
	plan, deployment := x.apply(yaml)
	steps := map[string]int{}
	for _, action := range plan.Actions {
		steps[action.Name] = action.Step
		t.Logf("plan action %s %s step %d", action.Op, action.Name, action.Step)
	}
	if deployment.Status != "succeeded" {
		t.Fatalf("composed deployment: %+v", deployment)
	}
	for _, step := range deployment.Steps {
		t.Logf("deployment step %s: %s %s", step.ActionID, step.Status, step.Message)
	}
	bound := x.bindings()
	byName := map[string]*planning.Binding{}
	for _, b := range bound {
		byName[b.Name] = b
	}
	for _, name := range []string{"app", "app--migrate", "app--api", "app--postgres"} {
		if byName[name] == nil {
			t.Fatalf("missing binding %s among %d bindings", name, len(bound))
		}
	}
	t.Logf("bindings after apply: %d (%s)", len(bound), strings.Join(sortedKeys(byName), ", "))
	api, postgres := byName["app--api"], byName["app--postgres"]
	environment := api.EnvironmentID
	apiIID, err := id.Parse(api.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	postgresIID, err := id.Parse(postgres.ExternalID)
	if err != nil {
		t.Fatal(err)
	}

	// Check 1: the api became ready through its declared HTTP probe.
	x.checkReadiness(apiIID)

	// Check 3, read first since it reports what checks 1 and 2 need: a
	// control port answers over the target's private channel, through the
	// admin proxy and through CatalogAccess directly.
	status := x.checkChannel(environment)
	if status.ReadyAnswered < 1 {
		t.Fatalf("check 1: /readyz never answered ready: %+v", status)
	}
	t.Logf("check 1 PASS: /readyz on the http port refused %d times during warm-up and answered ready %d times", status.ReadyRefused, status.ReadyAnswered)

	// Check 2: the migrate job ran once and succeeded before the api.
	if steps["app--migrate"] >= steps["app--api"] {
		t.Fatalf("check 2: migrate step %d isn't before api step %d", steps["app--migrate"], steps["app--api"])
	}
	if status.MigrateRuns != 1 || status.MigratedAt.IsZero() || !status.MigratedAt.Before(status.StartedAt) {
		t.Fatalf("check 2: migration record: %+v", status)
	}
	for _, step := range deployment.Steps {
		if strings.HasSuffix(step.ActionID, ":app--migrate") && step.Status != "succeeded" {
			t.Fatalf("check 2: migrate step: %+v", step)
		}
	}
	x.logJobEvidence(deployment.ID)
	t.Logf("check 2 PASS: one migrate run at %s, api started at %s (plan steps migrate=%d api=%d)", status.MigratedAt.Format(time.RFC3339Nano), status.StartedAt.Format(time.RFC3339Nano), steps["app--migrate"], steps["app--api"])

	// Check 4: removal deletes every binding and runtime object, and keeps
	// the provisioned database's volume.
	_, removed := x.apply("version: 1\n")
	if removed.Status != "succeeded" {
		t.Fatalf("check 4: removal: %+v", removed)
	}
	if left := x.bindings(); len(left) != 0 {
		t.Fatalf("check 4: bindings remain: %+v", left)
	}
	x.checkRemoval(postgresIID)
}

// apply saves a manifest, plans it as a catalog plan, approves it and
// waits for its deployment.
func (x *composedRun) apply(yaml string) (planBody, deploymentBody) {
	t, c := x.t, x.c
	base := "/api/v1/projects/" + x.projectID
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": yaml}, nil)
	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]any{"environment": "production", "catalog": true}, &plan)
	waitPlanned(t, c, &plan)
	if !plan.Executable || plan.Status != "ready" {
		t.Fatalf("plan %s: %+v", plan.ID, plan)
	}
	c.must(http.StatusOK, http.MethodPost, "/api/v1/plans/"+plan.ID+"/approve", map[string]string{"revision": plan.Revision}, nil)
	var d deploymentBody
	for deadline := time.Now().Add(time.Minute); d.ID == ""; {
		if time.Now().After(deadline) {
			t.Fatal("approved plan did not create a deployment")
		}
		c.do(http.MethodGet, "/api/v1/plans/"+plan.ID+"/deployment", nil, &d)
		time.Sleep(200 * time.Millisecond)
	}
	d = waitInfrastructureDeployment(t, c, d.ID)
	if d.Status != "succeeded" {
		var logs realtime.LogPage
		c.do(http.MethodGet, "/api/v1/deployments/"+d.ID+"/logs?limit=1000", nil, &logs)
		for _, line := range logs.Logs {
			t.Logf("deployment log: %s", line.Message)
		}
	}
	return plan, d
}

func (x *composedRun) bindings() []*planning.Binding {
	var list struct {
		Bindings []*planning.Binding `json:"bindings"`
	}
	x.c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+x.projectID+"/bindings?environment=production", nil, &list)
	return list.Bindings
}

// checkReadiness finds the api's runtime object and checks it declares
// the HTTP probe on /readyz at port 8080 and is ready.
func (x *composedRun) checkReadiness(iid id.ID) {
	t := x.t
	if x.provider == "server" {
		name := server.ContainerName(iid)
		raw := fixtureCommand(t, "docker", "exec", x.state.Builder, "docker", "inspect", name)
		var info []struct {
			Config struct {
				Labels map[string]string `json:"Labels"`
			} `json:"Config"`
			State struct {
				Running bool `json:"Running"`
			} `json:"State"`
			NetworkSettings struct {
				Ports map[string][]struct {
					HostIP   string `json:"HostIp"`
					HostPort string `json:"HostPort"`
				} `json:"Ports"`
			} `json:"NetworkSettings"`
		}
		if err := json.Unmarshal([]byte(raw), &info); err != nil || len(info) != 1 {
			t.Fatalf("inspect %s: %v", name, err)
		}
		labels := info[0].Config.Labels
		if !info[0].State.Running || labels[owned.LabelHealth] != "/readyz" || labels[owned.LabelHealthPort] != "8080" || labels[owned.LabelHealthProtocol] != "" {
			t.Fatalf("check 1: api container %s: running=%t health=%q port=%q protocol=%q", name, info[0].State.Running, labels[owned.LabelHealth], labels[owned.LabelHealthPort], labels[owned.LabelHealthProtocol])
		}
		for port, published := range info[0].NetworkSettings.Ports {
			for _, p := range published {
				if p.HostIP != "127.0.0.1" {
					t.Fatalf("check 3: %s published beyond loopback: %+v", port, p)
				}
			}
			t.Logf("api container %s publishes %s on %+v", name, port, published)
		}
		t.Logf("check 1 evidence: container %s running with %s=%s %s=%s", name, owned.LabelHealth, labels[owned.LabelHealth], owned.LabelHealthPort, labels[owned.LabelHealthPort])
		return
	}
	name := kubernetes.WorkloadName(iid)
	raw := fixtureCommand(t, append(slices.Clone(x.kubectl), "-n", x.state.Namespace, "get", "deployment", name, "-o", "json")...)
	var workload struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name           string `json:"name"`
						ReadinessProbe *struct {
							HTTPGet *struct {
								Path string `json:"path"`
								Port any    `json:"port"`
							} `json:"httpGet"`
						} `json:"readinessProbe"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
		Status struct {
			ReadyReplicas int `json:"readyReplicas"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &workload); err != nil || len(workload.Spec.Template.Spec.Containers) == 0 {
		t.Fatalf("deployment %s: %v", name, err)
	}
	probe := workload.Spec.Template.Spec.Containers[0].ReadinessProbe
	if probe == nil || probe.HTTPGet == nil || probe.HTTPGet.Path != "/readyz" || fmt.Sprint(probe.HTTPGet.Port) != "8080" || workload.Status.ReadyReplicas < 1 {
		t.Fatalf("check 1: deployment %s: probe %+v ready replicas %d", name, probe, workload.Status.ReadyReplicas)
	}
	t.Logf("check 1 evidence: deployment %s has readinessProbe httpGet %s port %v and %d ready replicas", name, probe.HTTPGet.Path, probe.HTTPGet.Port, workload.Status.ReadyReplicas)
}

// checkChannel reads the api's control port through the admin proxy and
// through CatalogAccess.InternalChannel, and returns what it reports.
func (x *composedRun) checkChannel(environment string) acceptanceStatus {
	t, c := x.t, x.c
	var session catalogproxy.Session
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+x.projectID+"/environments/"+environment+"/catalog/app/admin-sessions", nil, &session)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, x.base+session.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		t.Fatalf("check 3: admin proxy: HTTP %d %s", response.StatusCode, body)
	}
	var proxied acceptanceStatus
	if err = json.Unmarshal(body, &proxied); err != nil || proxied.Component != "api" || !proxied.AdminToken {
		t.Fatalf("check 3: admin proxy answer %q: %v", body, err)
	}
	t.Logf("check 3 evidence: admin proxy (InternalChannel) GET /admin/status: HTTP %d %s", response.StatusCode, strings.TrimSpace(string(body)))

	ref := deployments.ResourceRef{ProjectID: x.projectID, EnvironmentID: environment, Resource: "app"}
	token, err := x.access.InternalSecret(t.Context(), x.orgID, ref, "admin-token")
	if err != nil || len(token) == 0 {
		t.Fatalf("check 3: generated admin token: %v", err)
	}
	channel, err := x.access.InternalChannel(t.Context(), x.orgID, deployments.ComponentRef{ResourceRef: ref, Component: "api", Port: "admin"})
	if err != nil {
		t.Fatalf("check 3: open the private channel: %v", err)
	}
	defer func() { _ = channel.Close() }()
	request, err = http.NewRequestWithContext(t.Context(), http.MethodGet, channel.BaseURL.String()+"/admin/status", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(acceptanceAdminHeader, string(token))
	response, err = channel.Client.Do(request)
	if err != nil {
		t.Fatalf("check 3: request over the private channel: %v", err)
	}
	body, err = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		t.Fatalf("check 3: private channel: HTTP %d %s", response.StatusCode, body)
	}
	var status acceptanceStatus
	if err = json.Unmarshal(body, &status); err != nil || status.Component != "api" {
		t.Fatalf("check 3: private channel answer %q: %v", body, err)
	}
	// The routable port isn't a control port, so no channel reaches it.
	if _, err = x.access.InternalChannel(t.Context(), x.orgID, deployments.ComponentRef{ResourceRef: ref, Component: "api", Port: "http"}); !errors.Is(err, deployments.ErrNotControlPort) {
		t.Fatalf("check 3: a channel to the http port: %v", err)
	}
	t.Logf("check 3 PASS: CatalogAccess.InternalChannel GET http://component/admin/status: HTTP %d %s", response.StatusCode, strings.TrimSpace(string(body)))
	return status
}

// logJobEvidence records what the migrate job left behind: its retained
// output and, on Kubernetes, its Job object.
func (x *composedRun) logJobEvidence(deploymentID string) {
	t := x.t
	var logs realtime.LogPage
	x.c.must(http.StatusOK, http.MethodGet, "/api/v1/deployments/"+deploymentID+"/logs?limit=1000", nil, &logs)
	for _, line := range logs.Logs {
		if strings.Contains(line.Message, "LPACCEPT_") || strings.Contains(strings.ToLower(line.Message), "migrate") {
			t.Logf("deployment log: %s", line.Message)
		}
	}
	selector := owned.LabelProject + "=" + x.projectID
	if x.provider == "server" {
		t.Logf("server containers for the project: %q", fixtureCommand(t, "docker", "exec", x.state.Builder, "docker", "ps", "-a", "--filter", "label="+selector, "--format", "{{.Names}} {{.Status}} {{.Label \""+owned.LabelCatalogComponent+"\"}}"))
		return
	}
	t.Logf("cluster jobs for the project: %q", fixtureCommand(t, append(slices.Clone(x.kubectl), "-n", x.state.Namespace, "get", "jobs", "-l", selector, "-o", "wide", "--ignore-not-found")...))
	t.Logf("cluster pods for the project: %q", fixtureCommand(t, append(slices.Clone(x.kubectl), "-n", x.state.Namespace, "get", "pods", "-l", selector, "-o", "wide")...))
}

// checkRemoval checks nothing of the project runs on the target and the
// provisioned Postgres's data volume is kept, as database retention says.
func (x *composedRun) checkRemoval(postgres id.ID) {
	t := x.t
	selector := owned.LabelProject + "=" + x.projectID
	var volumes struct {
		Volumes []struct {
			Name       string `json:"name"`
			InstanceID string `json:"instance_id"`
			ProjectID  string `json:"project_id"`
			Role       string `json:"role"`
			Status     string `json:"status"`
		} `json:"volumes"`
	}
	x.c.must(http.StatusOK, http.MethodGet, "/api/v1/connections/"+x.connection+"/storage", nil, &volumes)
	retained := false
	for _, v := range volumes.Volumes {
		t.Logf("storage inventory: %+v", v)
		retained = retained || (v.InstanceID == postgres.String() && v.Role == "data" && v.Status == "retained")
	}
	if !retained {
		t.Fatalf("check 4: storage inventory doesn't list the provisioned database's volume as retained: %+v", volumes.Volumes)
	}
	if x.provider == "server" {
		if left := fixtureCommand(t, "docker", "exec", x.state.Builder, "docker", "ps", "-aq", "--filter", "label="+selector); left != "" {
			t.Fatalf("check 4: containers remain: %s", left)
		}
		kept := fixtureCommand(t, "docker", "exec", x.state.Builder, "docker", "volume", "ls", "--filter", "label="+owned.LabelInstance+"="+postgres.String(), "--format", "{{.Name}}")
		if kept != server.VolumeName(postgres, "data") {
			t.Fatalf("check 4: database volume: %q", kept)
		}
		t.Logf("check 4 PASS: no containers labelled %s; database volume %s kept", selector, kept)
		return
	}
	left := fixtureCommand(t, append(slices.Clone(x.kubectl), "-n", x.state.Namespace, "get", "deployment,statefulset,job,service,pod,secret,configmap", "-l", selector, "-o", "name")...)
	if left != "" {
		t.Fatalf("check 4: objects remain: %s", left)
	}
	t.Logf("cluster claims for the project after removal: %q", fixtureCommand(t, append(slices.Clone(x.kubectl), "-n", x.state.Namespace, "get", "pvc", "-l", selector, "-o", "name")...))
	claim := owned.PersistentVolumeName(postgres, "data")
	raw := fixtureCommand(t, append(slices.Clone(x.kubectl), "-n", x.state.Namespace, "get", "pvc", claim, "-o", "jsonpath={.status.phase} {.spec.volumeName}")...)
	fields := strings.Fields(raw)
	if len(fields) != 2 || fields[0] != "Bound" {
		t.Fatalf("check 4: database claim %s: %q", claim, raw)
	}
	reclaim := fixtureCommand(t, append(slices.Clone(x.kubectl), "get", "pv", fields[1], "-o", "jsonpath={.spec.persistentVolumeReclaimPolicy}")...)
	if reclaim != "Retain" {
		t.Fatalf("check 4: database volume %s reclaim policy %q", fields[1], reclaim)
	}
	t.Logf("check 4 PASS: no deployment, statefulset, job, service, pod, secret or configmap labelled %s; claim %s kept on volume %s (reclaim %s)", selector, claim, fields[1], reclaim)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

var pushedDigest = regexp.MustCompile(`digest: (sha256:[0-9a-f]{64})`)

// acceptanceImage builds the fixture command for both architectures,
// builds a scratch image for each in the run's own Docker daemon and
// pushes it to the run's registry. The digests come from the pushes.
func acceptanceImage(t *testing.T, state infrastructureState) recipes.Image {
	t.Helper()
	repository := "localhost:" + strconv.Itoa(state.RegistryPort) + "/lpaccept/catalog"
	image := recipes.Image{Repository: repository, Digests: map[string]string{}}
	for _, architecture := range []string{"amd64", "arm64"} {
		dir := t.TempDir()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
		build := exec.CommandContext(ctx, "go", "build", "-tags", "acceptance", "-trimpath", "-o", filepath.Join(dir, "catalog"), "../acceptance/infrastructure/catalog")
		build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+architecture, "CGO_ENABLED=0")
		output, err := build.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("build the fixture for %s: %v %s", architecture, err, output)
		}
		if err = os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\nCOPY catalog /catalog\nUSER 65532:65532\nENTRYPOINT [\"/catalog\"]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// The daemon mounts a tmpfs over /tmp, which would hide the copy.
		staged := "/root/lpaccept-" + architecture
		fixtureCommand(t, "docker", "cp", dir, state.Builder+":"+staged)
		tag := repository + ":" + architecture
		fixtureCommand(t, "docker", "exec", state.Builder, "docker", "build", "--platform", "linux/"+architecture, "--provenance=false", "--sbom=false", "-t", tag, staged)
		pushed := fixtureCommand(t, "docker", "exec", state.Builder, "docker", "push", tag)
		match := pushedDigest.FindStringSubmatch(pushed)
		if match == nil {
			t.Fatalf("push %s reported no digest: %s", tag, pushed)
		}
		image.Digests[architecture] = match[1]
		t.Logf("pushed %s as %s@%s", tag, repository, match[1])
	}
	return image
}

// registerAcceptanceKind adds the acceptance kind to the catalog this
// process's app reads, and its admin to the proxy.
func registerAcceptanceKind(t *testing.T, image recipes.Image) {
	t.Helper()
	if _, ok := recipes.Default().Kind(acceptanceKind); ok {
		t.Fatal("the acceptance kind is already registered in this process; run with -count=1")
	}
	database := recipes.EnvVar{Name: "DATABASE_URL", From: &recipes.Ref{Dependency: "postgres", Field: recipes.FieldURL}}
	err := recipes.Default().Register(recipes.Kind{
		Name:        acceptanceKind,
		Title:       "Acceptance catalog",
		Description: "A test-only kind: a migration job and an API with a control port, on a provisioned Postgres.",
		Placement:   recipes.PlacementAppProject,
		Targets:     []string{"server", "kubernetes"},
		Releases: []recipes.Release{{
			Version: "1", Product: "1.0.0", Revision: "1",
			Images: map[string]recipes.Image{"migrate": image, "api": image},
		}},
		Dependencies: []recipes.Dependency{{Name: "postgres", Title: "PostgreSQL", Kinds: []string{"postgres"}}},
		Secrets:      []recipes.GeneratedSecret{{Name: "admin-token"}},
		Components: []recipes.Component{
			{
				Name:    "migrate",
				Role:    recipes.RoleJob,
				Command: []string{"/catalog", "migrate"},
				Env:     []recipes.EnvVar{database},
				Timeout: 5 * time.Minute,
			},
			{
				Name:    "api",
				Role:    recipes.RoleProcess,
				Command: []string{"/catalog", "serve"},
				Ports: []recipes.Port{
					{Name: "http", Container: 8080, Protocol: "http"},
					{Name: "admin", Container: 9090, Protocol: "http", Control: true},
				},
				Env:       []recipes.EnvVar{database, {Name: "ADMIN_TOKEN", Secret: "admin-token"}},
				Readiness: &recipes.HTTPProbe{Port: "http", Path: "/readyz"},
				After:     []string{"migrate"},
			},
		},
		Outputs: []recipes.Output{{Name: "url", EnvKey: "LPACCEPT_URL", From: &recipes.Ref{Component: "api", Field: "url:http"}}},
	})
	if err != nil {
		t.Fatalf("register the acceptance kind: %v", err)
	}
	registerAcceptanceAdmin.Do(func() {
		catalogproxy.Register(acceptanceKind, catalogproxy.Target{
			Component: "api", Port: "admin", Root: "/admin/status", AllowedPrefixes: []string{"/admin/"},
			Header: func(_ context.Context, secret func(string) ([]byte, error)) (http.Header, error) {
				token, err := secret("admin-token")
				if err != nil {
					return nil, err
				}
				return http.Header{acceptanceAdminHeader: {string(token)}}, nil
			},
		})
	})
}

// retainedStorageClass makes a storage class like kind's default that
// retains its volumes, as a provisioned Postgres on Kubernetes needs. The
// cluster, and the class with it, goes when the run's cleanup deletes it.
func retainedStorageClass(t *testing.T, state infrastructureState, kubectl []string) string {
	t.Helper()
	name := state.RunID + "-retain"
	provisioner := fixtureCommand(t, append(slices.Clone(kubectl), "get", "storageclass", "standard", "-o", "jsonpath={.provisioner}")...)
	manifest := filepath.Join(t.TempDir(), "storageclass.yaml")
	body := fmt.Sprintf("apiVersion: storage.k8s.io/v1\nkind: StorageClass\nmetadata:\n  name: %s\n  labels:\n    loomplane.acceptance: %s\nprovisioner: %s\nreclaimPolicy: Retain\nvolumeBindingMode: WaitForFirstConsumer\n", name, state.RunID, provisioner)
	if err := os.WriteFile(manifest, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureCommand(t, append(slices.Clone(kubectl), "apply", "-f", manifest)...)
	return name
}

//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime/pprof"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"golang.org/x/crypto/ssh"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/planning"
	apiclient "github.com/xraph/loomplane/pkg/client"
)

type combinedHost struct {
	Container string `json:"container"`
	Daemon    string `json:"daemon"`
	Address   string `json:"address"`
	HostKey   string `json:"host_key"`
}
type combinedState struct {
	RunID     string                  `json:"run_id"`
	Hosts     map[string]combinedHost `json:"hosts"`
	Addresses map[string]string       `json:"addresses"`
}

func combinedSSH(t *testing.T, host combinedHost, private []byte) *ssh.Client {
	t.Helper()
	signer, err := ssh.ParsePrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(host.HostKey))
	if err != nil {
		t.Fatal(err)
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(host.Address, "22"), &ssh.ClientConfig{User: "root", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(key), HostKeyAlgorithms: []string{key.Type()}, Timeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}
func combinedExec(t *testing.T, connection *ssh.Client, input []byte, command string) string {
	t.Helper()
	session, err := connection.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	session.Stdin = bytes.NewReader(input)
	output, err := session.CombinedOutput(command)
	if err != nil {
		t.Fatalf("fixture SSH command failed: %v: %s", err, output)
	}
	return strings.TrimSpace(string(output))
}
func combinedQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func combinedDNS(t *testing.T, address, name, kind, value string) time.Time {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"name": name, "values": map[string][]string{kind: {value}}})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+address+":8055/records", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("fixture DNS status %d", res.StatusCode)
	}
	changedAt, err := time.Parse(time.RFC3339Nano, res.Header.Get("X-Fixture-Changed-At"))
	if err != nil {
		t.Fatal(err)
	}
	return changedAt
}

func combinedHTTPS(ctx context.Context, hostname, address, marker, container string) error {
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: hostname}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(address, "443"))
	}}
	defer transport.CloseIdleConnections()
	request, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+hostname+"/", nil)
	if reqErr != nil {
		return reqErr
	}
	response, getErr := (&http.Client{Transport: transport, Timeout: 10 * time.Second}).Do(request)
	if getErr != nil {
		return getErr
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return readErr
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), marker) || !strings.Contains(string(body), container[:12]) {
		return fmt.Errorf("wrong project/release response: %s", body)
	}
	if response.TLS.PeerCertificates[0].NotAfter.Before(time.Now().Add(24 * time.Hour)) {
		return fmt.Errorf("fixture certificate lifetime below production floor")
	}
	return nil
}

const combinedExternalTTLSeconds = 60

func combinedDrainBound(cutover, deadline, now time.Time, succeeded bool) error {
	if cutover.IsZero() || deadline.Before(cutover.Add(combinedExternalTTLSeconds*time.Second)) {
		return fmt.Errorf("source drain deadline %s precedes independent cutover+TTL bound %s", deadline, cutover.Add(combinedExternalTTLSeconds*time.Second))
	}
	if succeeded && now.Before(cutover.Add(combinedExternalTTLSeconds*time.Second)) {
		return fmt.Errorf("migration succeeded before independent cutover+TTL bound")
	}
	return nil
}

type combinedSourceObservation struct {
	At          time.Time `json:"at"`
	ContainerID string    `json:"container_id"`
	Running     bool      `json:"running"`
	Hostname    string    `json:"hostname"`
	Marker      string    `json:"marker"`
}

func combinedObserveSource(ctx context.Context, connection *ssh.Client, hostname, address, marker, container string, until time.Time) ([]combinedSourceObservation, error) {
	var observations []combinedSourceObservation
	for time.Now().Before(until) {
		if err := ctx.Err(); err != nil {
			return observations, err
		}
		probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		stopClose := context.AfterFunc(probeCtx, func() { _ = connection.Close() })
		session, err := connection.NewSession()
		var output []byte
		if err == nil {
			output, err = session.CombinedOutput("docker inspect --format '{{.Id}} {{.State.Running}}' " + combinedQuote(container))
			_ = session.Close()
		}
		stopClose()
		cancel()
		if err != nil {
			return observations, fmt.Errorf("source inspection: %w", err)
		}
		if strings.TrimSpace(string(output)) != container+" true" {
			return observations, fmt.Errorf("source container lost during independent TTL interval: %s", output)
		}
		if err = combinedHTTPS(ctx, hostname, address, marker, container); err != nil {
			return observations, err
		}
		observations = append(observations, combinedSourceObservation{At: time.Now(), ContainerID: container, Running: true, Hostname: hostname, Marker: marker})
		select {
		case <-ctx.Done():
			return observations, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return observations, nil
}

func TestSharedCloudCombinedDrainBound(t *testing.T) {
	cutover := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name                 string
		deadline, elapsed    time.Duration
		succeeded, wantError bool
	}{
		{"expired", -time.Second, 5 * time.Second, false, true},
		{"shortened", 5 * time.Second, 10 * time.Second, false, true},
		{"premature_success", time.Minute, 59 * time.Second, true, true},
		{"valid_pending", time.Minute, 5 * time.Second, false, false},
		{"exact_bound", time.Minute, time.Minute, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := combinedDrainBound(cutover, cutover.Add(test.deadline), cutover.Add(test.elapsed), test.succeeded)
			if (err != nil) != test.wantError {
				t.Fatalf("guard error=%v, want error=%v", err, test.wantError)
			}
		})
	}
}

func TestAcceptanceSharedCloudCombined(t *testing.T) {
	statePath := os.Getenv("LOOMPLANE_SHARED_CLOUD_STATE")
	if statePath == "" {
		t.Skip("run tests/acceptance/shared-clouds/run.py")
	}
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state combinedState
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(state.RunID, "lp-shared-") || len(state.Hosts) != 2 {
		t.Fatal("invalid combined ownership record")
	}
	directory := filepath.Dir(statePath)
	private, err := os.ReadFile(filepath.Join(directory, "ssh"))
	if err != nil {
		t.Fatal(err)
	}
	transportRoot, err := os.ReadFile(filepath.Join(directory, "transport.pem"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, running, c, org, first := cloudAPISetup(t)
	defer func() { running.stop() }()
	db, err := forge.InjectType[*grove.DB](running.app.(forge.App).Container())
	if err != nil {
		t.Fatal(err)
	}
	store := cloudinfra.NewPgStore(pgdriver.Unwrap(db))
	ledger := map[string]any{"run_id": state.RunID, "org_id": org, "database": strings.TrimPrefix(strings.Split(cfg.Database.URL, "?")[0], "postgres://fixture:fixture@database:5432/"), "hosts": map[string]any{}, "projects": []string{first}}
	record := func() {
		t.Helper()
		data, marshalErr := json.MarshalIndent(ledger, "", "  ")
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if writeErr := os.WriteFile(filepath.Join(directory, "application-ledger.json"), data, 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	record()
	var cloud cloudprofiles.Profile
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+first+"/clouds", cloudprofiles.CreateInput{Name: "Combined fixture"}, &cloud)
	cloudBase := "/api/v1/clouds/" + cloud.ID
	servers := map[string]cloudinfra.Server{}
	connections := map[string]string{}
	pools := map[string]cloudinfra.Pool{}
	for _, role := range []string{"host1", "host2"} {
		host := state.Hosts[role]
		settings := map[string]string{"host": host.Address, "advertise_host": host.Address, "user": "root", "host_key": host.HostKey}
		var connection struct{ ID, Status string }
		c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "server", "name": role, "credential": string(private), "settings": settings}, &connection)
		if connection.Status != "ready" {
			t.Fatalf("SSH/Docker connection %s is %s", role, connection.Status)
		}
		connections[role] = connection.ID
		var operation cloudinfra.Operation
		c.must(http.StatusOK, http.MethodPost, cloudBase+"/servers/preview", cloudinfra.ServerInput{Kind: "adopt", Name: role, ConnectionID: connection.ID, CloudRevision: cloud.Revision, ConfirmAdoption: true}, &operation)
		ledger["hosts"].(map[string]any)[role] = map[string]any{"parent_container": host.Container, "daemon": host.Daemon, "operation": operation.ID, "server": operation.ServerID}
		record()
		c.must(http.StatusAccepted, http.MethodPost, cloudBase+"/infrastructure-operations/"+operation.ID+"/apply", cloudinfra.ApplyInput{Review: operation.Review}, &operation)
		for deadline := time.Now().Add(time.Minute); ; time.Sleep(100 * time.Millisecond) {
			c.must(http.StatusOK, http.MethodGet, cloudBase+"/infrastructure-operations/"+operation.ID, nil, &operation)
			if operation.Status == "succeeded" {
				break
			}
			if operation.Status == "failed" || time.Now().After(deadline) {
				t.Fatalf("adopt %s: %+v", role, operation)
			}
		}
		var listing struct{ Servers []cloudinfra.Server }
		c.must(http.StatusOK, http.MethodGet, cloudBase+"/servers", nil, &listing)
		for _, candidate := range listing.Servers {
			if candidate.ID == operation.ServerID {
				servers[role] = candidate
			}
		}
		hostRecord := ledger["hosts"].(map[string]any)[role].(map[string]any)
		hostRecord["server"] = operation.ServerID
		record()
		var pool cloudinfra.Pool
		c.must(http.StatusOK, http.MethodPost, cloudBase+"/pools", cloudinfra.PoolInput{Name: role, Members: []cloudinfra.PoolMember{{ServerID: operation.ServerID, CPUMillis: 2000, MemoryBytes: 1 << 30}}}, &pool)
		pools[role] = pool
		epoch, epochErr := store.BeginServerIngress(t.Context(), org, operation.ServerID)
		if epochErr != nil {
			t.Fatal(epochErr)
		}
		sshConfig, parseErr := server.ParseConfig(private, settings)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		sshConfig.Details["docker_id"] = host.Daemon
		remote, dialErr := server.Dial(t.Context(), sshConfig)
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		remote.RequireFence = true
		fence := server.WithEffectFence(t.Context(), server.EffectFence{Operation: org + "/ingress/" + operation.ServerID, Generation: epoch.Generation})
		initial, ensureErr := server.EnsureIngress(fence, remote, org, operation.ServerID)
		_ = remote.Close()
		if ensureErr != nil {
			t.Fatal(ensureErr)
		}
		hostRecord["proxy"] = initial.ContainerID
		hostRecord["initial_config"] = initial.Config
		record()
		fixtureSSH := combinedSSH(t, host, private)
		inspected := combinedExec(t, fixtureSSH, nil, "docker inspect "+combinedQuote(initial.ContainerID))
		var inspection []struct {
			Config struct{ Image string }
			Mounts []struct{ Type, Name string }
		}
		if err = json.Unmarshal([]byte(inspected), &inspection); err != nil {
			t.Fatal(err)
		}
		if len(inspection) != 1 || inspection[0].Config.Image != server.IngressImage {
			t.Fatal("ingress pin changed")
		}
		hostRecord["mounts"] = inspection[0].Mounts
		record()
		combinedExec(t, fixtureSSH, transportRoot, "docker exec -i "+combinedQuote(initial.ContainerID)+" sh -c 'cat >> /etc/ssl/certs/ca-certificates.crt'")
		combinedExec(t, fixtureSSH, nil, "docker restart "+combinedQuote(initial.ContainerID))
	}
	projects := make([]string, 0, 3)
	projects = append(projects, first)
	for _, name := range []string{"Unrelated project", "Other host project"} {
		var project struct{ ID string }
		c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": name, "repository_url": "https://github.com/acme/mono"}, &project)
		projects = append(projects, project.ID)
		ledger["projects"] = projects
		record()
		c.must(http.StatusOK, http.MethodPut, cloudBase+"/projects/"+project.ID, map[string]any{"target_ids": []string{}}, nil)
	}
	environments := map[string]string{}
	targets := map[string]cloudprofiles.Target{}
	bindings := map[string]planning.Binding{}
	markers := map[string]string{}
	variables := map[string]string{}
	routeContainers := map[string]string{}
	hostnames := map[string]string{}
	roles := []string{"host1", "host1", "host2"}
	binding := func(project string) planning.Binding {
		t.Helper()
		var result struct{ Bindings []planning.Binding }
		c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+project+"/bindings?environment=production", nil, &result)
		for _, b := range result.Bindings {
			if b.Name == "web" {
				return b
			}
		}
		t.Fatal("web binding missing")
		return planning.Binding{}
	}
	manifest := func(target, health string) string {
		return fmt.Sprintf("version: 1\nservices:\n  web:\n    source: node:apps/web\n    cloud_target: %s\n    public: false\n    port: 80\n    health_path: %s\n    size: {cpu: 0.2, memory: 128}\n    build: {image: traefik/whoami:v1.11.0@sha256:200689790a0a0ea48ca45992e0450bc26ccab5307375b41c84dfc4f2475937ab}\nignore:\n  resources: [postgres, redis]\n", target, health)
	}
	var duringDeployment func(deploymentBody)
	deploy := func(project, target, health string) deploymentBody {
		t.Helper()
		base := "/api/v1/projects/" + project
		c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": manifest(target, health)}, nil)
		var plan planBody
		c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
		waitPlanned(t, c, &plan)
		if !plan.Executable {
			t.Fatalf("unexecutable combined plan: %+v", plan)
		}
		persisted, readPlanErr := planning.NewPgStore(pgdriver.Unwrap(db)).GetPlan(t.Context(), org, plan.ID)
		if readPlanErr != nil {
			t.Fatal(readPlanErr)
		}
		ledger["private-plan-"+project] = persisted.Actions
		ledger["plan-"+project] = plan.ID
		record()
		c.must(http.StatusOK, http.MethodPost, "/api/v1/plans/"+plan.ID+"/approve", map[string]string{"revision": plan.Revision}, nil)
		var result deploymentBody
		for deadline := time.Now().Add(150 * time.Second); ; time.Sleep(250 * time.Millisecond) {
			status := c.do(http.MethodGet, "/api/v1/plans/"+plan.ID+"/deployment", nil, &result)
			if status == http.StatusOK && duringDeployment != nil {
				duringDeployment(result)
			}
			if status == http.StatusOK && (result.Status == "succeeded" || result.Status == "failed" || result.Status == "canceled") {
				return result
			}
			c.must(http.StatusOK, http.MethodGet, "/api/v1/plans/"+plan.ID, nil, &plan)
			if plan.Status == "stale" || plan.Status == "failed" || time.Now().After(deadline) {
				t.Fatalf("combined plan/deployment: %+v %+v", plan, result)
			}
		}
	}
	var zone cloudinfra.Zone
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/dns-zones", cloudinfra.ZoneInput{Name: state.RunID + ".test", Mode: "external", ProjectIDs: projects}, &zone)
	combinedDNS(t, state.Addresses["gateway"], zone.ProofName, "TXT", zone.ProofValue)
	c.must(http.StatusOK, http.MethodPost, "/api/v1/dns-zones/"+zone.ID+"/verify", nil, &zone)
	verifyLimits := func(role, container string) {
		t.Helper()
		output := combinedExec(t, combinedSSH(t, state.Hosts[role], private), nil, "docker inspect "+combinedQuote(container))
		var inspected []struct {
			ID         string
			HostConfig struct{ NanoCpus, Memory int64 }
		}
		if inspectErr := json.Unmarshal([]byte(output), &inspected); inspectErr != nil {
			t.Fatal(inspectErr)
		}
		if len(inspected) != 1 || inspected[0].ID != container || inspected[0].HostConfig.NanoCpus != 200000000 || inspected[0].HostConfig.Memory != 134217728 {
			t.Fatalf("runtime limits do not match 0.2 CPU / 128 MiB: %+v", inspected)
		}
		ledger["runtime-limits-"+container] = inspected[0]
		record()
	}
	for index, project := range projects {
		base := "/api/v1/projects/" + project
		analyze(t, cfg, c, project)
		var envs struct{ Environments []struct{ ID, Name string } }
		c.must(http.StatusOK, http.MethodGet, base+"/environments", nil, &envs)
		for _, e := range envs.Environments {
			if e.Name == "production" {
				environments[project] = e.ID
			}
		}
		var target cloudprofiles.Target
		c.must(http.StatusCreated, http.MethodPost, base+"/clouds/"+cloud.ID+"/targets", cloudprofiles.CreateTargetInput{Name: fmt.Sprintf("app%d", index), Provider: "server", Runtime: "server", PoolID: pools[roles[index]].ID}, &target)
		targets[project] = target
		var preview cloudprofiles.Preview
		c.must(http.StatusOK, http.MethodPost, base+"/environments/"+environments[project]+"/cloud/preview", apiclient.EnvironmentCloudInput{CloudID: cloud.ID}, &preview)
		if result := waitCloudAPI(t, c, base, applyCloudAPI(t, c, base, &preview, "assign-"+project).ID); result.Status != cloudprofiles.SyncSucceeded {
			t.Fatalf("assignment: %+v", result)
		}
		marker := state.RunID + "-" + project + "-release-one"
		markers[project] = marker
		var variable struct{ ID string }
		c.must(http.StatusCreated, http.MethodPost, base+"/variables", map[string]any{"key": "WHOAMI_NAME", "value": marker, "environment": "production"}, &variable)
		variables[project] = variable.ID
		if result := deploy(project, target.Name, "/"); result.Status != "succeeded" {
			t.Fatalf("deploy %+v", result)
		}
		bindings[project] = binding(project)
		hostname := fmt.Sprintf("app%d.%s", index, zone.Name)
		hostnames[project] = hostname
		combinedDNS(t, state.Addresses["gateway"], hostname, "A", state.Hosts[roles[index]].Address)
		var route cloudinfra.RouteOperation
		c.must(http.StatusOK, http.MethodPost, base+"/domain-routes/preview", cloudinfra.RouteInput{ZoneID: zone.ID, CloudID: cloud.ID, EnvironmentID: environments[project], Service: "web", Hostname: hostname, Port: 80, ExternalTTLSeconds: combinedExternalTTLSeconds}, &route)
		c.must(http.StatusAccepted, http.MethodPost, base+"/route-operations/"+route.ID+"/apply", cloudinfra.ApplyInput{Review: route.Review}, &route)
		for deadline := time.Now().Add(90 * time.Second); ; time.Sleep(250 * time.Millisecond) {
			c.must(http.StatusOK, http.MethodGet, base+"/route-operations/"+route.ID, nil, &route)
			if route.Status == "succeeded" {
				break
			}
			if route.Status == "waiting" {
				status := c.do(http.MethodPost, base+"/route-operations/"+route.ID+"/retry", nil, nil)
				if status != http.StatusAccepted && status != http.StatusConflict {
					t.Fatalf("route retry: HTTP %d", status)
				}
			}
			if route.Status == "failed" || time.Now().After(deadline) {
				t.Fatalf("route: %+v", route)
			}
		}
		verifyLimits(roles[index], route.Route.Binding.ContainerID)
		routeContainers[project] = route.Route.Binding.ContainerID
		ledger["route-"+project] = route
		ledger["binding-"+project] = bindings[project]
		record()
	}

	verify := func(project, address, marker string) {
		t.Helper()
		if probeErr := combinedHTTPS(t.Context(), hostnames[project], address, marker, routeContainers[project]); probeErr != nil {
			t.Fatal(probeErr)
		}
	}
	for index, project := range projects {
		verify(project, state.Hosts[roles[index]].Address, markers[project])
	}
	ledger["three_project_https"] = "passed"
	record()
	metricsPollers := func() int {
		t.Helper()
		var profile bytes.Buffer
		if profileErr := pprof.Lookup("goroutine").WriteTo(&profile, 2); profileErr != nil {
			t.Fatal(profileErr)
		}
		return strings.Count(profile.String(), "github.com/xraph/ctrlplane/metrics.(*service).runPoller(")
	}
	pollerMeasurements := [][2]int{}
	restart := func() {
		t.Helper()
		before := metricsPollers()
		running.stop()
		for deadline := time.Now().Add(5 * time.Second); metricsPollers() != 0; time.Sleep(50 * time.Millisecond) {
			if time.Now().After(deadline) {
				break // CtrlPlane v1.8.0 owns an independent metrics context.
			}
		}
		pollerMeasurements = append(pollerMeasurements, [2]int{before, metricsPollers()})
		ledger["restart_metrics_pollers_before_after"] = pollerMeasurements
		record()
		running = start(t, cfg)
		c.base = running.srv.URL
		db, err = forge.InjectType[*grove.DB](running.app.(forge.App).Container())
		if err != nil {
			t.Fatal(err)
		}
	}
	base := "/api/v1/projects/" + first
	target := targets[first]
	var migrationPreview cloudprofiles.Preview
	c.must(http.StatusOK, http.MethodPost, base+"/cloud-changes/preview", cloudprofiles.ChangeInput{CloudID: cloud.ID, TargetID: target.ID, Revision: target.Revision, Provider: "server", Runtime: "server", PoolID: pools["host2"].ID}, &migrationPreview)
	changedParticipants := 0
	for _, environment := range migrationPreview.Environments {
		changes := environment.Summary.Create + environment.Summary.Update + environment.Summary.Replace + environment.Summary.Delete + environment.Summary.Adopt
		if changes > 0 {
			changedParticipants++
			if environment.ProjectID != first || !reflect.DeepEqual(environment.Services, []string{"web"}) {
				t.Fatalf("migration changes unrelated service: %+v", environment)
			}
		}
	}
	if changedParticipants != 1 {
		t.Fatalf("migration changed participants: %d", changedParticipants)
	}
	migration := applyCloudAPI(t, c, base, &migrationPreview, "migrate-current")
	ledger["migration"] = migration.ID
	record()
	changedDNS, restartedPrepared, restartedDrain := false, false, false
	var prepared cloudinfra.RouteOperation
	var retainedUntil, cutoverAt, minimumUntil time.Time
	var sourceObservations []combinedSourceObservation
	var sourceObserverErr error
	var sourceDone chan struct{}
	sourceConnection := combinedSSH(t, state.Hosts["host1"], private)
	sourceContainer := routeContainers[first]
	observerContext, cancelObserver := context.WithCancel(t.Context())
	defer func() {
		cancelObserver()
		if sourceDone != nil {
			<-sourceDone
		}
		ledger["source_retention_observations"] = sourceObservations
		if sourceObserverErr != nil {
			ledger["source_retention_error"] = sourceObserverErr.Error()
		}
		record()
	}()

	for deadline := time.Now().Add(4 * time.Minute); ; time.Sleep(time.Second) {
		c.must(http.StatusOK, http.MethodGet, base+"/cloud-syncs/"+migration.ID, nil, migration)
		verify(projects[1], state.Hosts["host1"].Address, markers[projects[1]])
		if migration.Status == cloudprofiles.SyncFailed || time.Now().After(deadline) {
			t.Fatalf("combined migration: %+v", migration)
		}
		for _, env := range migration.Environments {
			for _, route := range env.Routes {
				if route.PrepareID != "" {
					c.must(http.StatusOK, http.MethodGet, base+"/route-operations/"+route.PrepareID, nil, &prepared)
				}
				if prepared.Status == "succeeded" && !changedDNS {
					routeContainers[first] = prepared.Route.Binding.ContainerID
					verify(first, state.Hosts["host2"].Address, markers[first])
					routeContainers[first] = sourceContainer
					verify(first, state.Hosts["host1"].Address, markers[first])
					if !restartedPrepared {
						restart()
						restartedPrepared = true
					}
					cutoverAt = combinedDNS(t, state.Addresses["gateway"], hostnames[first], "A", state.Hosts["host2"].Address)
					minimumUntil = cutoverAt.Add(combinedExternalTTLSeconds * time.Second)
					ledger["dns_cutover_at"] = cutoverAt
					ledger["minimum_retention_until"] = minimumUntil
					sourceDone = make(chan struct{})
					go func(hostname, address, marker, container string, until time.Time) {
						defer close(sourceDone)
						sourceObservations, sourceObserverErr = combinedObserveSource(observerContext, sourceConnection, hostname, address, marker, container, until)
					}(hostnames[first], state.Hosts["host1"].Address, markers[first], sourceContainer, minimumUntil)
					changedDNS = true
					ledger["migration-prepared"] = prepared
					record()
				}
				if !route.NotBefore.IsZero() {
					retainedUntil = route.NotBefore
					if boundErr := combinedDrainBound(cutoverAt, retainedUntil, time.Now(), false); boundErr != nil {
						t.Fatal(boundErr)
					}
					if !restartedDrain {
						ledger["drain_restart_started_at"] = time.Now()
						restart()
						ledger["drain_restart_completed_at"] = time.Now()
						restartedDrain = true
						ledger["retained_until"] = retainedUntil
						record()
					}
				}
			}
		}
		if !minimumUntil.IsZero() && time.Now().Before(minimumUntil) {
			verify(first, state.Hosts["host1"].Address, markers[first])
		}
		if migration.Status == cloudprofiles.SyncSucceeded {
			if boundErr := combinedDrainBound(cutoverAt, retainedUntil, time.Now(), true); boundErr != nil {
				t.Fatal(boundErr)
			}
			break
		}
	}
	if !changedDNS || !restartedPrepared || !restartedDrain {
		t.Fatal("migration skipped a required durable restart boundary")
	}
	<-sourceDone
	if sourceObserverErr != nil {
		t.Fatal(sourceObserverErr)
	}
	if len(sourceObservations) < 2 || sourceObservations[0].At.After(cutoverAt.Add(time.Second)) || sourceObservations[len(sourceObservations)-1].At.Before(minimumUntil.Add(-time.Second)) {
		t.Fatalf("source observer did not cover the independent TTL interval: %v", sourceObservations)
	}
	duringRestart := false
	for index, observation := range sourceObservations {
		if index > 0 && observation.At.Sub(sourceObservations[index-1].At) > 2*time.Second {
			t.Fatal("source retention observer missed more than two seconds")
		}
		if observation.At.After(ledger["drain_restart_started_at"].(time.Time)) && observation.At.Before(ledger["drain_restart_completed_at"].(time.Time)) {
			duringRestart = true
		}
	}
	if !duringRestart {
		t.Fatal("no source retention observation during drain restart")
	}
	ledger["source_retention_observations"] = sourceObservations
	record()
	migrated := binding(first)
	if migrated.ExternalID == bindings[first].ExternalID || migrated.ConnectionID != connections["host2"] {
		t.Fatal("migration did not move the exact instance to host2")
	}
	var beforeConfig, afterConfig planning.ServiceConfig
	if err = json.Unmarshal(bindings[first].Config, &beforeConfig); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(migrated.Config, &afterConfig); err != nil {
		t.Fatal(err)
	}
	if beforeConfig.Source != afterConfig.Source || beforeConfig.CommitSHA != afterConfig.CommitSHA || !reflect.DeepEqual(beforeConfig.AppliedInputs, afterConfig.AppliedInputs) || !reflect.DeepEqual(beforeConfig.BuildTarget, afterConfig.BuildTarget) {
		t.Fatal("current migration changed applied source, artifact or environment snapshot")
	}
	verifyLimits("host2", prepared.Route.Binding.ContainerID)
	routeContainers[first] = prepared.Route.Binding.ContainerID
	verify(first, state.Hosts["host2"].Address, markers[first])
	ledger["current_migration"] = "passed"
	ledger["migrated_binding"] = migrated
	record()
	nextMarker := state.RunID + "-" + first + "-release-two"
	c.must(http.StatusOK, http.MethodPatch, "/api/v1/variables/"+variables[first], map[string]string{"value": nextMarker}, nil)
	if result := deploy(first, target.Name, "/api"); result.Status != "succeeded" {
		t.Fatalf("future release: %+v", result)
	}
	future := binding(first)
	if future.ConnectionID != connections["host2"] || future.ExternalID == migrated.ExternalID {
		t.Fatal("future release did not replace on host2")
	}
	var routes struct{ Routes []cloudinfra.Route }
	c.must(http.StatusOK, http.MethodGet, base+"/domain-routes", nil, &routes)
	if len(routes.Routes) != 1 || routes.Routes[0].Input.Hostname != hostnames[first] || routes.Routes[0].Binding.InstanceID != future.ExternalID {
		t.Fatal("future release lost exact route identity")
	}
	verifyLimits("host2", routes.Routes[0].Binding.ContainerID)
	routeContainers[first] = routes.Routes[0].Binding.ContainerID
	verify(first, state.Hosts["host2"].Address, nextMarker)
	verify(projects[1], state.Hosts["host1"].Address, markers[projects[1]])
	ledger["future_release"] = "passed"
	ledger["future_binding"] = future
	record()
	// A wrong backend port holds readiness until the fixture kills the exact
	// recorded candidate. The accepted container remains independently healthy.
	fixtureHost := combinedSSH(t, state.Hosts["host2"], private)
	projectContainers := func() []string {
		t.Helper()
		return strings.Fields(combinedExec(t, fixtureHost, nil, "docker ps -aq --no-trunc --filter "+combinedQuote("label=loomplane.project="+first)))
	}
	baseline := projectContainers()
	var badPort struct{ ID string }
	c.must(http.StatusCreated, http.MethodPost, base+"/variables", map[string]any{"key": "WHOAMI_PORT_NUMBER", "value": "81", "environment": "production"}, &badPort)
	candidate := ""
	duringDeployment = func(_ deploymentBody) {
		verify(first, state.Hosts["host2"].Address, nextMarker)
		verify(projects[1], state.Hosts["host1"].Address, markers[projects[1]])
		if candidate != "" {
			return
		}
		for _, id := range projectContainers() {
			if slices.Contains(baseline, id) {
				continue
			}
			if candidate != "" {
				t.Fatal("more than one new failed-release candidate")
			}
			candidate = id
		}
		if candidate == "" {
			return
		}
		ledger["failed_candidate"] = candidate
		record()
		var grant cloudprofiles.ProjectGrant
		c.must(http.StatusOK, http.MethodDelete, cloudBase+"/projects/"+first, nil, &grant)
		if !grant.Revoked {
			t.Fatal("public grant revoke did not persist")
		}
		ledger["revoked_grant"] = grant
		record()
		combinedExec(t, fixtureHost, nil, "docker kill "+combinedQuote(candidate))
	}
	failed := deploy(first, target.Name, "/health")
	duringDeployment = nil
	if failed.Status != "failed" || candidate == "" {
		t.Fatalf("failed candidate not exercised: %+v", failed)
	}
	if slices.Contains(projectContainers(), candidate) {
		t.Fatal("recorded candidate cleanup failed after grant revoke")
	}
	if kept := binding(first); kept.ExternalID != future.ExternalID {
		t.Fatal("failed candidate replaced the accepted binding")
	}
	verify(first, state.Hosts["host2"].Address, nextMarker)
	verify(projects[2], state.Hosts["host2"].Address, markers[projects[2]])
	ledger["failed_candidate_cleanup_after_revoke"] = failed
	record()
	var deniedPlan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &deniedPlan)
	waitPlanned(t, c, &deniedPlan)
	ledger["plan_after_revoke"] = deniedPlan
	record()
	if deniedPlan.Executable {
		t.Fatal("revoked grant admitted a fresh executable plan")
	}
	c.must(http.StatusOK, http.MethodGet, base+"/domain-routes", nil, &routes)
	if len(routes.Routes) != 1 || routes.Routes[0].Binding.InstanceID != future.ExternalID {
		t.Fatal("failed candidate changed accepted route")
	}
	failedPlan := ledger["plan-"+first].(string)
	var attempt struct {
		Plan, Deployment         string
		Active, Settled, Effects bool
	}
	if err = pgdriver.Unwrap(db).QueryRow(t.Context(), `SELECT plan_id,deployment_id,active,settled,effects_started FROM loomplane_workload_attempts WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND service='web'`, org, first, environments[first]).Scan(&attempt.Plan, &attempt.Deployment, &attempt.Active, &attempt.Settled, &attempt.Effects); err != nil {
		t.Fatal(err)
	}
	ledger["attempt_before_route_removal"] = attempt
	record()
	if attempt.Plan != failedPlan || attempt.Deployment != failed.ID || attempt.Active || attempt.Settled || !attempt.Effects {
		t.Fatalf("unexpected failed workload guard: %+v", attempt)
	}
	removePath := base + "/domain-routes/" + routes.Routes[0].ID + "/remove"
	requestBody, err := json.Marshal(map[string]any{"revision": routes.Routes[0].Revision})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, c.base+removePath, bytes.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Publishable-Key", c.key)
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	ledger["route_removal_before_recovery"] = map[string]any{"status": response.StatusCode, "body": string(responseBody)}
	record()
	if response.StatusCode != http.StatusConflict || !strings.Contains(string(responseBody), "finish or recover the service deployment before changing its routes") {
		t.Fatalf("unexpected route removal refusal: %d %s", response.StatusCode, responseBody)
	}
	verify(first, state.Hosts["host2"].Address, nextMarker)
	var recovery cloudinfra.ReservationRecovery
	input := cloudinfra.RecoveryInput{PlanID: failedPlan, Service: "web"}
	c.must(http.StatusOK, http.MethodPost, cloudBase+"/placements/preview-recovery", input, &recovery)
	ledger["reservation_recovery_review"] = recovery
	record()
	if recovery.PlanID != failedPlan || recovery.ServerID != servers["host2"].ID || !recovery.Started || recovery.Active || recovery.Review == "" {
		t.Fatalf("unexpected recovery review: %+v", recovery)
	}
	input.Review = recovery.Review
	c.must(http.StatusOK, http.MethodPost, cloudBase+"/placements/recover", input, &recovery)
	ledger["reservation_recovery_after_revoke"] = recovery
	record()
	if recovery.Phase != "released" || !recovery.StorageRetained {
		t.Fatalf("recorded reservation recovery failed: %+v", recovery)
	}
	if err = pgdriver.Unwrap(db).QueryRow(t.Context(), `SELECT settled FROM loomplane_workload_attempts WHERE org_id=$1 AND plan_id=$2 AND service='web'`, org, failedPlan).Scan(&attempt.Settled); err != nil || !attempt.Settled {
		t.Fatalf("recovered workload not settled: %+v %v", attempt, err)
	}
	c.must(http.StatusOK, http.MethodGet, base+"/domain-routes", nil, &routes)
	if len(routes.Routes) != 1 || routes.Routes[0].Binding.InstanceID != future.ExternalID || binding(first).ExternalID != future.ExternalID {
		t.Fatal("reservation recovery changed accepted route or binding")
	}
	verify(first, state.Hosts["host2"].Address, nextMarker)
	verify(projects[2], state.Hosts["host2"].Address, markers[projects[2]])
	var removed cloudinfra.RouteOperation
	c.must(http.StatusAccepted, http.MethodPost, base+"/domain-routes/"+routes.Routes[0].ID+"/remove", map[string]any{"revision": routes.Routes[0].Revision}, &removed)
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(250 * time.Millisecond) {
		c.must(http.StatusOK, http.MethodGet, base+"/route-operations/"+removed.ID, nil, &removed)
		verify(projects[2], state.Hosts["host2"].Address, markers[projects[2]])
		if removed.Status == "succeeded" {
			break
		}
		if removed.Status == "failed" || time.Now().After(deadline) {
			t.Fatalf("route cleanup after revoke: %+v", removed)
		}
	}
	proxy := ledger["hosts"].(map[string]any)["host2"].(map[string]any)["proxy"].(string)
	configAfterRemoval := combinedExec(t, fixtureHost, nil, "docker exec "+combinedQuote(proxy)+" wget -qO- http://127.0.0.1:2019/config/")
	if strings.Contains(configAfterRemoval, hostnames[first]) || !strings.Contains(configAfterRemoval, hostnames[projects[2]]) {
		t.Fatal("route cleanup changed the wrong proxy configuration")
	}
	verifyLimits("host2", routeContainers[first])
	ledger["route_cleanup_after_revoke"] = removed
	record()
}

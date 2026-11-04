//go:build qualification

package deployments

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"
	"github.com/xraph/trove"
	"github.com/xraph/trove/drivers/localdriver"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/infrastructure"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

// This fixture exercises Ctrl Plane creation, server Runtime.Apply and the
// actual OwnedTarget backup/clone dispatch against an explicitly selected daemon.
func TestQualifyNativeEngineRecovery(t *testing.T) {
	socket := os.Getenv("LOOMPLANE_TEST_DOCKER_SOCKET")
	if socket == "" {
		t.Skip("explicit local Docker socket required")
	}
	selected := os.Getenv("LOOMPLANE_ENGINE")
	if selected == "" {
		t.Fatal("select one engine per qualification run")
	}
	architecture := runtime.GOARCH
	if value := os.Getenv("LOOMPLANE_ENGINE_ARCH"); value != "" {
		architecture = value
	}
	kind, ok := recipes.Default().Kind(selected)
	if selected == "duckdb" {
		repository := os.Getenv("LOOMPLANE_DUCKDB_REPOSITORY")
		amd, arm := os.Getenv("LOOMPLANE_DUCKDB_AMD64"), os.Getenv("LOOMPLANE_DUCKDB_ARM64")
		if repository == "" || amd == "" || arm == "" {
			t.Fatal("qualified local DuckDB manifest pins required")
		}
		kind = recipes.DuckDBKind(recipes.Image{Repository: repository, Digests: map[string]string{"amd64": amd, "arm64": arm}})
		ok = true
	}
	if !ok {
		t.Fatal("unknown qualification engine")
	}
	registry := recipes.NewRegistry()
	if err := registry.Register(kind); err != nil {
		t.Fatal(err)
	}
	comp, err := registry.Resolve(selected, "", architecture, "server")
	if err != nil {
		t.Fatal(err)
	}
	fixture := newComponentFixture(t, registry)
	target := fixture.target
	env := "recovery-" + id.New(id.PrefixInstance).String()
	principal := security.Principal{OrgID: "org", UserID: "usr"}
	ctx, cancel := context.WithTimeout(infrastructure.WithPrincipal(t.Context(), principal), 8*time.Minute)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &server.Client{Config: &server.Config{Details: map[string]string{"docker_id": "task5-qualification"}}, HTTP: &http.Client{Transport: transport}}
	backend := &server.Runtime{Session: func(context.Context, map[string]string) (*server.Client, error) { return client, nil }, Values: target.values, Wait: 3 * time.Minute}
	target.Backend = func(string, string) owned.Backend { return backend }
	cfg := &planning.ResourceConfig{Kind: selected, Version: comp.Version, Provider: "server", Target: "conn", TargetSettings: map[string]string{"architecture": architecture}, Component: &planning.ComponentConfig{Kind: selected, Resource: "source", Revision: comp.Revision, Component: comp.Components[0], Protection: recipes.ProtectionNative}}
	binding := &planning.Binding{OrgID: "org", ProjectID: "proj", EnvironmentID: env, Name: "source", Provider: "server", ConnectionID: "conn", Subject: planning.ResourceSubject}
	desc, err := recovery.Describe(cfg, registry)
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := target.recoveryRun(ctx, binding, cfg, desc, "source")
	if err != nil {
		t.Fatal(err)
	}
	run.Recovery = false
	rendered, err := target.renderFiles(ctx, kind, run)
	if err != nil {
		t.Fatal(err)
	}
	ref := resourceKey("org", env, "source", "runtime_qualification")
	raw, err := json.Marshal(run.Env)
	if err != nil {
		t.Fatal(err)
	}
	if err = fixture.secrets.Put(ctx, ref, raw); err != nil {
		t.Fatal(err)
	}
	if err = target.keepRuntimeFiles(ctx, ref, rendered); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{owned.LabelOrg: "org", owned.LabelProject: "proj", owned.LabelEnvironment: env, owned.LabelConnection: "conn", owned.LabelService: "source", owned.LabelRuntime: ref, owned.LabelRuntimeFiles: "true", owned.LabelPublic: "false", owned.LabelArchitecture: architecture, owned.LabelImage: desc.Image, owned.LabelRevision: "qualification-source"}
	spec := cpprovider.ServiceSpec{Name: mainService, Image: desc.Image, Env: map[string]string{}}
	componentSpec(&spec, labels, cfg.Component, "server")
	for i := range spec.Volumes {
		spec.Volumes[i].SizeMB = 0
	}
	provider := target.Register("org", "conn")
	var sourceID id.ID
	var classInstances []id.ID
	operation, _ := typeid.Generate("restore")
	docker := func(input string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "docker", append([]string{"--host", "unix://" + socket}, args...)...)
		cmd.Stdin = strings.NewReader(input)
		return cmd.CombinedOutput()
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(infrastructure.WithPrincipal(context.Background(), principal), time.Minute)
		defer stop()
		defer func() {
			fallback, cancelFallback := context.WithTimeout(infrastructure.WithPrincipal(context.Background(), principal), time.Minute)
			defer cancelFallback()
			for _, kind := range []string{"container", "volume"} {
				args := []string{kind, "ls", "-q", "--filter", "label=" + owned.LabelEnvironment + "=" + env}
				if kind == "container" {
					args = append(args, "-a")
				}
				raw, e := exec.CommandContext(fallback, "docker", append([]string{"--host", "unix://" + socket}, args...)...).Output()
				if e != nil {
					t.Error(e)
					continue
				}
				for _, item := range strings.Fields(string(raw)) {
					remove := []string{kind, "rm"}
					if kind == "container" {
						remove = append(remove, "-f")
					}
					remove = append(remove, item)
					if e = exec.CommandContext(fallback, "docker", append([]string{"--host", "unix://" + socket}, remove...)...).Run(); e != nil {
						t.Error(e)
					}
				}
			}
			// Fixture workers have finished. Production lifecycle never deletes anchors.
			for _, instanceID := range classInstances {
				anchorName := server.ContainerName(instanceID) + "-class"
				raw, readErr := exec.CommandContext(fallback, "docker", "--host", "unix://"+socket, "volume", "inspect", anchorName).Output()
				if readErr != nil {
					continue
				}
				var anchors []struct {
					Name, Driver    string
					Labels, Options map[string]string
				}
				if json.Unmarshal(raw, &anchors) != nil || len(anchors) != 1 || anchors[0].Name != anchorName || anchors[0].Driver != "local" || len(anchors[0].Options) != 0 || anchors[0].Labels["loomplane.instance-class.environment"] != env || anchors[0].Labels["loomplane.instance-class.instance"] != instanceID.String() {
					t.Error("fixture class anchor changed; retained")
					continue
				}
				if removeErr := exec.CommandContext(fallback, "docker", "--host", "unix://"+socket, "volume", "rm", anchorName).Run(); removeErr != nil {
					t.Error(removeErr)
				}
			}
			for _, network := range []string{server.NetworkName("org", env), server.NetworkName("org", env+"/recovery/"+operation.String())} {
				_ = exec.CommandContext(fallback, "docker", "--host", "unix://"+socket, "network", "rm", network).Run()
			}
		}()
		if binding.ExternalID != "" {
			if deleteErr := target.DeleteRecovery(cleanup, principal, binding, operation.String()); deleteErr != nil {
				t.Error(deleteErr)
			}
			if removeErr := backend.Remove(cleanup, sourceID, labels); removeErr != nil {
				t.Error(removeErr)
			}
			for _, args := range [][]string{{"volume", "rm", server.VolumeName(sourceID, "data")}, {"network", "rm", server.NetworkName("org", env)}, {"network", "rm", server.NetworkName("org", env+"/recovery/"+operation.String())}} {
				_ = exec.CommandContext(cleanup, "docker", append([]string{"--host", "unix://" + socket}, args...)...).Run()
			}
		}
	})
	source, err := target.Ctrl.Instances.Create(ctx, instance.CreateRequest{Name: "task5-source-" + strings.TrimPrefix(env, "recovery-"), ProviderName: provider, Services: []cpprovider.ServiceSpec{spec}, Labels: labels})
	if err != nil {
		t.Fatal(err)
	}
	sourceID = source.ID
	classInstances = append(classInstances, sourceID)
	binding.ExternalID = sourceID.String()
	binding.Config, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sourceName := server.ContainerName(sourceID)
	query := func(container, user, password, sql, method, endpoint string) ([]byte, error) {
		switch selected {
		case "falkordb":
			command := "GRAPH.RO_QUERY"
			if method == "write" {
				command = "GRAPH.QUERY"
			}
			// Credentials travel through stdin to a fixed shell, never process arguments.
			input := fmt.Sprintf("export REDISCLI_AUTH='%s'\nexec redis-cli --no-auth-warning -e --raw --user %s %s lifecycle '%s'\n", password, user, command, sql)
			return docker(input, "exec", "-i", container, "sh")
		case "duckdb":
			input := "SET extension_directories=['/opt/duckdb/extensions']; LOAD httpfs; LOAD quack; SET enable_server_cert_verification=true; SET ca_cert_file='/etc/loomplane/ca.pem'; ATTACH 'quack:localhost:9494' AS remote (TOKEN '" + password + "', DISABLE_SSL false); CONNECT remote;\n" + sql + "\n.quit\n"
			return docker(input, "exec", "-i", container, "/duckdb", "-batch", "-bail", "-csv", "-noheader")
		default:
			scheme, ca := "http", ""
			if selected == "opensearch" {
				scheme = "https"
				ca = "cacert = \"/usr/share/opensearch/config/ca.pem\"\n"
			}
			input := fmt.Sprintf("url = %q\nuser = %q\nmax-time = 15\n%s", scheme+"://localhost:9200"+endpoint, user+":"+password, ca)
			args := []string{"exec", "-i", container, "curl", "--silent", "--show-error", "--fail", "--config", "-", "--request", method}
			if sql != "" {
				args = append(args, "--header", "Content-Type: application/json", "--data-binary", sql)
			}
			return docker(input, args...)
		}
	}
	sourcePassword := string(run.Secrets["password"])
	admin := "default"
	if selected == "duckdb" {
		sourcePassword = string(run.Secrets["token"])
	}
	if selected == "elasticsearch" {
		admin = "elastic"
	}
	if selected == "opensearch" {
		admin = "loomplane"
	}
	mustQuery := func(container, user, password, sql, method, endpoint string) []byte {
		out, e := query(container, user, password, sql, method, endpoint)
		if e != nil {
			t.Fatalf("native %s %s query failed: %v: %s", method, endpoint, e, bytes.ReplaceAll(out, []byte(password), []byte("[redacted]")))
		}
		return out
	}
	searchTemplate := "loomplane-task5a-" + env
	searchMetadata := "cluster.metadata.loomplane_task5a_" + env
	searchConfigPath := "/.opensearch-sap-log-types-config"
	var sourceConfigCreated int64
	searchConfigCreationDate := func(raw []byte) int64 {
		t.Helper()
		var indices map[string]struct {
			Settings map[string]string `json:"settings"`
		}
		if decodeErr := json.Unmarshal(raw, &indices); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		created, parseErr := strconv.ParseInt(indices[".opensearch-sap-log-types-config"].Settings["index.creation_date"], 10, 64)
		if parseErr != nil || created <= 0 {
			t.Fatal("plugin configuration creation date is invalid")
		}
		return created
	}
	searchCount := func(raw []byte) int64 {
		t.Helper()
		var result struct {
			Count *int64 `json:"count"`
		}
		if decodeErr := json.Unmarshal(raw, &result); decodeErr != nil || result.Count == nil {
			t.Fatal("native count response is invalid")
		}
		return *result.Count
	}
	switch selected {
	case "falkordb":
		mustQuery(sourceName, admin, sourcePassword, `CREATE (:Event {value:"historical-2024"}),(:Event {value:"recent-2026"})`, "write", "")
	case "duckdb":
		mustQuery(sourceName, admin, sourcePassword, "CREATE TABLE events(recorded_at TIMESTAMP, value VARCHAR); INSERT INTO events VALUES ('2024-01-01','historical-2024'),('2026-11-01','recent-2026');", "write", "")
	default:
		mustQuery(sourceName, admin, sourcePassword, `{"settings":{"number_of_replicas":0},"mappings":{"properties":{"value":{"type":"keyword"}}},"aliases":{"event-history":{}}}`, "PUT", "/events")
		mustQuery(sourceName, admin, sourcePassword, `{"value":"historical-2024"}`, "PUT", "/events/_doc/1?refresh=true")
		mustQuery(sourceName, admin, sourcePassword, `{"settings":{"index.hidden":true,"number_of_replicas":0},"mappings":{"properties":{"value":{"type":"keyword"}}},"aliases":{"hidden-history":{}}}`, "PUT", "/.user-history")
		mustQuery(sourceName, admin, sourcePassword, `{"value":"hidden-historical"}`, "PUT", "/.user-history/_doc/1?refresh=true")
		mustQuery(sourceName, admin, sourcePassword, fmt.Sprintf(`{"persistent":{%q:"source-only"}}`, searchMetadata), "PUT", "/_cluster/settings")
		mustQuery(sourceName, admin, sourcePassword, `{"index_patterns":["audit-history"],"data_stream":{},"template":{"settings":{"number_of_replicas":0},"mappings":{"properties":{"@timestamp":{"type":"date"},"value":{"type":"keyword"}}}}}`, "PUT", "/_index_template/"+searchTemplate)
		mustQuery(sourceName, admin, sourcePassword, `{"@timestamp":"2024-01-01T00:00:00Z","value":"stream-historical"}`, "POST", "/audit-history/_doc?refresh=true")
		if selected == "opensearch" {
			mustQuery(sourceName, admin, sourcePassword, `{"settings":{"index.hidden":true,"number_of_replicas":0}}`, "PUT", "/.opensearch-sap-log-types-config-user")
			mustQuery(sourceName, admin, sourcePassword, `{"value":"similar-prefix-history"}`, "PUT", "/.opensearch-sap-log-types-config-user/_doc/1?refresh=true")
			sourceConfigCreated = searchConfigCreationDate(mustQuery(sourceName, admin, sourcePassword, "", "GET", searchConfigPath+"/_settings?flat_settings=true"))
		}
	}
	driver := localdriver.New()
	dsn := "file://" + t.TempDir()
	if err = driver.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	store, err := trove.Open(driver)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	if err = store.CreateBucket(ctx, "backups"); err != nil {
		t.Fatal(err)
	}
	files, err := backupfiles.New(store, "backups", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if selected == "duckdb" {
		// A clean exit is insufficient when committed rows remain only in the WAL.
		mustQuery(sourceName, admin, sourcePassword, "PRAGMA disable_checkpoint_on_shutdown; INSERT INTO events VALUES ('2023-01-01','wal-only-history');", "write", "")
		if _, failure := target.BackupDatabase(ctx, principal, binding, files, "org/task5/wal-refusal"); failure == nil || !strings.Contains(failure.Error(), "WAL") {
			t.Fatalf("remaining WAL was not refused: %v", failure)
		}
		if failure := backend.DatabaseProbe(ctx, sourceID, labels); failure != nil {
			t.Fatalf("WAL refusal did not resume source: %v", failure)
		}
		survived := mustQuery(sourceName, admin, sourcePassword, "SELECT value FROM events WHERE value='wal-only-history';", "read", "")
		if !bytes.Contains(survived, []byte("wal-only-history")) {
			t.Fatal("WAL refusal lost committed rows")
		}
		mustQuery(sourceName, admin, sourcePassword, "PRAGMA enable_checkpoint_on_shutdown;", "write", "")

		// Simulate the documented orphan input on the exact owned source. This is
		// an operator-procedure test, not evidence of a killed remote worker.
		rawID, failure := docker("", "inspect", "--format", "{{.Id}}", sourceName)
		if failure != nil {
			t.Fatal(failure)
		}
		sourceContainerID := strings.TrimSpace(string(rawID))
		reservationName := sourceName + "-mutation"
		record := map[string]any{"version": 1, "owner": operation.String(), "action": "backup", "operation": "org/task5/operator-orphan", "Org": "org", "Project": "proj", "Environment": env, "Connection": "conn", "Instance": sourceID.String(), "Resource": "source", "SourceID": sourceContainerID, "SourceImage": desc.Image, "SourceRevision": labels[owned.LabelRevision], "SourceRunning": true}
		rawSource, failure := docker("", "inspect", sourceContainerID)
		if failure != nil {
			t.Fatal(failure)
		}
		var observed []struct {
			ID     string `json:"Id"`
			Image  string
			Config struct {
				Image  string
				Labels map[string]string
			}
		}
		if json.Unmarshal(rawSource, &observed) != nil || len(observed) != 1 || observed[0].ID != sourceContainerID || observed[0].Config.Image != desc.Image {
			t.Fatal("operator source identity missing")
		}
		sourceLabels := map[string]string{}
		for _, key := range []string{owned.LabelInstance, owned.LabelOrg, owned.LabelProject, owned.LabelEnvironment, owned.LabelConnection, owned.LabelService, owned.LabelRevision, owned.LabelImage, owned.LabelArchitecture, owned.LabelPublic, server.LabelRecovery, "loomplane.engine.recovery", "loomplane.engine.recipe-revision", "loomplane.engine.recovery-source"} {
			sourceLabels[key] = observed[0].Config.Labels[key]
		}
		record["SourceLabels"] = sourceLabels
		record["SourceDockerImage"] = observed[0].Image
		volumeName := server.VolumeName(sourceID, "data")
		rawVolume, failure := docker("", "volume", "inspect", volumeName)
		if failure != nil {
			t.Fatal(failure)
		}
		var volumes []struct {
			Name, Driver, CreatedAt string
			Labels, Options         map[string]string
		}
		if json.Unmarshal(rawVolume, &volumes) != nil || len(volumes) != 1 || volumes[0].Name != volumeName || volumes[0].Driver != "local" || volumes[0].CreatedAt == "" || len(volumes[0].Options) != 0 {
			t.Fatal("operator data volume identity missing")
		}
		for key, value := range map[string]string{owned.LabelOrg: "org", owned.LabelProject: "proj", owned.LabelEnvironment: env, owned.LabelConnection: "conn", owned.LabelInstance: sourceID.String(), owned.LabelVolumeRole: "data"} {
			if volumes[0].Labels[key] != value {
				t.Fatal("operator volume scope mismatch")
			}
		}
		record["Volume"], record["VolumeCreated"] = volumeName, volumes[0].CreatedAt
		encoded, failure := json.Marshal(record)
		if failure != nil {
			t.Fatal(failure)
		}
		create := exec.CommandContext(ctx, "docker", "--host", "unix://"+socket, "create", "--platform", "linux/"+architecture, "--name", reservationName, "--network", "none", "--read-only", "--entrypoint", "/loomplane-reservation-never-start", "--label", "loomplane.duckdb-mutation=1", "--label", "loomplane.duckdb-mutation.record="+string(encoded), desc.Image)
		reservationID, platformWarning, failure := qualificationContainerID(create)
		if failure != nil {
			t.Fatal(failure)
		}
		if platformWarning {
			t.Log("Docker create emitted a platform warning on stderr; stdout contained a valid container ID")
		}
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
			defer stop()
			raw, inspectErr := exec.CommandContext(cleanup, "docker", "--host", "unix://"+socket, "inspect", reservationID).Output()
			if inspectErr != nil {
				return
			}
			var held []struct {
				ID     string `json:"Id"`
				Name   string
				Config struct{ Labels map[string]string }
				State  struct{ Running bool }
				Mounts []any
			}
			if json.Unmarshal(raw, &held) != nil || len(held) != 1 || held[0].ID != reservationID || held[0].Name != "/"+reservationName || held[0].Config.Labels["loomplane.duckdb-mutation.record"] != string(encoded) || held[0].State.Running || len(held[0].Mounts) != 0 {
				t.Error("task reservation changed; retained")
				return
			}
			if removeErr := exec.CommandContext(cleanup, "docker", "--host", "unix://"+socket, "rm", reservationID).Run(); removeErr != nil {
				t.Error(removeErr)
			}
		})
		if _, failure = docker("", "stop", "--time", "25", sourceContainerID); failure != nil {
			t.Fatal(failure)
		}
		if failure = backend.SetRunning(ctx, sourceID, labels, true); failure == nil {
			t.Fatal("lifecycle bypassed orphan reservation")
		}
		// The qualification uses one drained local worker and checks the complete
		// owner record, source incarnation and absence of previous containers.
		rawLock, failure := docker("", "inspect", reservationID)
		if failure != nil {
			t.Fatal(failure)
		}
		var locks []struct {
			ID     string `json:"Id"`
			Name   string
			Config struct{ Labels map[string]string }
			State  struct{ Running bool }
			Mounts []any
		}
		if json.Unmarshal(rawLock, &locks) != nil || len(locks) != 1 || locks[0].ID != reservationID || locks[0].Name != "/"+reservationName || locks[0].Config.Labels["loomplane.duckdb-mutation.record"] != string(encoded) || locks[0].Config.Labels[owned.LabelInstance] != "" || locks[0].State.Running || len(locks[0].Mounts) != 0 {
			t.Fatal("operator reservation identity changed")
		}
		rawAnchor, failure := docker("", "volume", "inspect", sourceName+"-class")
		if failure != nil {
			t.Fatal(failure)
		}
		var anchors []struct {
			Name, Driver    string
			Labels, Options map[string]string
		}
		if json.Unmarshal(rawAnchor, &anchors) != nil || len(anchors) != 1 || anchors[0].Name != sourceName+"-class" || anchors[0].Driver != "local" || len(anchors[0].Options) != 0 {
			t.Fatal("operator class anchor missing")
		}
		for key, value := range map[string]string{"schema": "1", "class": "duckdb", "org": "org", "project": "proj", "environment": env, "connection": "conn", "instance": sourceID.String(), "resource": "source", "recovery": ""} {
			if anchors[0].Labels["loomplane.instance-class."+key] != value {
				t.Fatal("operator anchor scope mismatch")
			}
		}
		rawID, failure = docker("", "inspect", "--format", "{{.Id}}", sourceName)
		if failure != nil || strings.TrimSpace(string(rawID)) != sourceContainerID {
			t.Fatal("operator source incarnation changed")
		}
		for _, suffix := range []string{"-previous", "-previous-stopped"} {
			if _, failure = docker("", "inspect", sourceName+suffix); failure == nil {
				t.Fatal("operator found unresolved previous container")
			}
		}
		if _, failure = docker("", "stop", "--time", "25", sourceContainerID); failure != nil {
			t.Fatal(failure)
		}
		if _, failure = docker("", "start", sourceContainerID); failure != nil {
			t.Fatal(failure)
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, failure = docker("", "exec", sourceContainerID, "/loomplane-duckdb", "check"); failure == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("operator could not verify native readiness")
			}
			time.Sleep(100 * time.Millisecond)
		}
		rawLock, failure = docker("", "inspect", reservationID)
		if failure != nil || json.Unmarshal(rawLock, &locks) != nil || len(locks) != 1 || locks[0].Config.Labels["loomplane.duckdb-mutation.record"] != string(encoded) || locks[0].State.Running {
			t.Fatal("operator reservation changed before removal")
		}
		if _, failure = docker("", "rm", reservationID); failure != nil {
			t.Fatal(failure)
		}
		t.Log("WAL refusal retained committed rows and resumed source; exact-owner simulated orphan recovery passed")
	}

	object, err := target.BackupDatabase(ctx, principal, binding, files, "org/task5/backup")
	if err != nil {
		t.Fatal(err)
	}
	if err = backend.DatabaseProbe(ctx, sourceID, labels); err != nil {
		t.Fatalf("source did not recover after backup: %v", err)
	}
	copyID, err := target.RestoreDatabase(ctx, principal, binding, files, object, operation.String())
	if err != nil {
		t.Fatal(err)
	}
	cloneID, err := id.Parse(copyID)
	if err != nil {
		t.Fatal(err)
	}
	classInstances = append(classInstances, cloneID)
	clone, err := target.Ctrl.Instances.Get(ctx, cloneID)
	if err != nil {
		t.Fatal(err)
	}
	values, err := target.values(owned.WithRuntimeIdentity(ctx, owned.RuntimeIdentity{Instance: cloneID, Provider: "server"}), owned.InstanceLabels(clone.Labels, cloneID))
	if err != nil {
		t.Fatal(err)
	}
	reader := values.Env["LOOMPLANE_APP_PASSWORD"]
	if reader == "" || reader == sourcePassword {
		t.Fatal("clone did not isolate credentials")
	}
	user := "app"
	if selected == "elasticsearch" || selected == "opensearch" {
		user = "loomplane_recovery"
	}
	cloneName := server.ContainerName(cloneID)
	readSQL, readMethod, readPath := `MATCH(n:Event) RETURN n.value`, "read", ""
	writeSQL, writeMethod, writePath := `CREATE (:Event {value:"forbidden"})`, "write", ""
	if selected == "duckdb" {
		readSQL = "SELECT value FROM events ORDER BY recorded_at;"
		writeSQL = "INSERT INTO events VALUES(now(),'forbidden');"
	}
	if selected == "elasticsearch" || selected == "opensearch" {
		readSQL = ""
		readMethod = "GET"
		readPath = "/event-history/_search"
		writeSQL = `{"value":"forbidden"}`
		writeMethod = "PUT"
		writePath = "/events/_doc/2"
	}
	out := mustQuery(cloneName, user, reader, readSQL, readMethod, readPath)
	if selected == "duckdb" && !bytes.Contains(out, []byte("wal-only-history")) {
		t.Fatal("committed WAL row did not survive successful checkpoint restore")
	}
	if !bytes.Contains(out, []byte("historical-2024")) {
		t.Fatal("historical native data did not survive restore")
	}
	if _, err = query(cloneName, user, reader, writeSQL, writeMethod, writePath); err == nil {
		t.Fatal("recovery reader could write")
	}
	if _, err = query(cloneName, admin, sourcePassword, readSQL, readMethod, readPath); err == nil {
		t.Fatal("source credential works on clone")
	}
	if _, err = query(sourceName, user, reader, readSQL, readMethod, readPath); err == nil {
		t.Fatal("clone credential works on source")
	}
	if selected == "elasticsearch" || selected == "opensearch" {
		cloneAdminQuery := func(endpoint string) []byte {
			t.Helper()
			scheme := "http"
			if selected == "opensearch" {
				scheme = "https"
			}
			response, queryErr := docker("", "exec", cloneName, "sh", "-c", `sed '/^url = /d' "$1" | curl --silent --show-error --fail --config - --request GET --url "$2"`, "loomplane-qualification", "/usr/share/"+selected+"/config/loomplane-probe.conf", scheme+"://localhost:9200"+endpoint)
			if queryErr != nil {
				t.Fatalf("clone admin query failed: %v", queryErr)
			}
			return response
		}
		var settings struct {
			Persistent map[string]json.RawMessage `json:"persistent"`
		}
		if err := json.Unmarshal(cloneAdminQuery("/_cluster/settings?flat_settings=true"), &settings); err != nil {
			t.Fatal(err)
		}
		if _, exists := settings.Persistent[searchMetadata]; exists {
			t.Fatal("clone restored source cluster metadata")
		}
		var templates struct {
			Templates []struct {
				Name string `json:"name"`
			} `json:"index_templates"`
		}
		if err := json.Unmarshal(cloneAdminQuery("/_index_template"), &templates); err != nil {
			t.Fatal(err)
		}
		for _, template := range templates.Templates {
			if template.Name == searchTemplate {
				t.Fatal("clone restored source template")
			}
		}
		if selected == "opensearch" {
			if searchCount(cloneAdminQuery(searchConfigPath+"/_count")) == 0 {
				t.Fatal("clone plugin configuration was not initialized")
			}
			if searchConfigCreationDate(cloneAdminQuery(searchConfigPath+"/_settings?flat_settings=true")) <= sourceConfigCreated {
				t.Fatal("clone restored source plugin configuration")
			}
			out = mustQuery(cloneName, user, reader, "", "GET", "/.opensearch-sap-log-types-config-user/_doc/1")
			if !bytes.Contains(out, []byte("similar-prefix-history")) {
				t.Fatal("similar-prefix hidden user index missing")
			}
		}
		if selected == "opensearch" {
			out = cloneAdminQuery("/events/_mapping")
		} else {
			out = mustQuery(cloneName, user, reader, "", "GET", "/events/_mapping")
		}
		if !bytes.Contains(out, []byte("keyword")) {
			t.Fatal("mapping missing")
		}
		out = mustQuery(cloneName, user, reader, "", "GET", "/.user-history/_doc/1")
		if !bytes.Contains(out, []byte("hidden-historical")) {
			t.Fatal("hidden user index missing")
		}
		out = mustQuery(cloneName, user, reader, "", "GET", "/audit-history/_search")
		if !bytes.Contains(out, []byte("stream-historical")) {
			t.Fatal("data stream history missing")
		}
		if _, e := query(cloneName, user, reader, `{"@timestamp":"2026-11-01T00:00:00Z","value":"forbidden"}`, "POST", "/audit-history/_doc"); e == nil {
			t.Fatal("recovery reader wrote to data stream")
		}
	}
	if selected == "duckdb" || selected == "opensearch" {
		sourceValues, err := target.values(owned.WithRuntimeIdentity(ctx, owned.RuntimeIdentity{Instance: sourceID, Provider: "server"}), owned.InstanceLabels(labels, sourceID))
		if err != nil {
			t.Fatal(err)
		}
		ca := "/etc/loomplane/ca.pem"
		if selected == "opensearch" {
			ca = "/usr/share/opensearch/config/ca.pem"
		}
		if bytes.Equal(sourceValues.Files[ca], values.Files[ca]) {
			t.Fatal("clone reused source CA")
		}
	}
	t.Logf("actual native dispatch, isolated readonly clone, historical content and credential separation passed for %s/%s (%d bytes)", selected, architecture, object.Size)
}

// qualificationContainerID keeps Docker diagnostics separate from its result.
func qualificationContainerID(command *exec.Cmd) (string, bool, error) {
	var diagnostics bytes.Buffer
	command.Stderr = &diagnostics
	stdout, runErr := command.Output()
	platformWarning := strings.Contains(diagnostics.String(), "requested image's platform")
	if runErr != nil {
		return "", platformWarning, fmt.Errorf("create qualification reservation: %w", runErr)
	}
	containerID := strings.TrimSpace(string(stdout))
	if len(containerID) != 64 || strings.Trim(containerID, "0123456789abcdef") != "" {
		return "", platformWarning, fmt.Errorf("Docker create returned an invalid container ID")
	}
	return containerID, platformWarning, nil
}

func TestQualificationContainerIDSeparatesDiagnostics(t *testing.T) {
	containerID := strings.Repeat("a", 64)
	warning := "WARNING: The requested image's platform (linux/amd64) does not match the detected host platform (linux/arm64/v8)\n"
	for _, tc := range []struct {
		name, stdout, stderr, exitCode string
		wantID, wantWarning            bool
	}{
		{name: "valid stdout with platform warning", stdout: containerID + "\n", stderr: warning, wantID: true, wantWarning: true},
		{name: "valid stdout without diagnostics", stdout: containerID + "\n", wantID: true},
		{name: "warning mixed into stdout", stdout: warning + containerID + "\n"},
		{name: "truncated ID", stdout: containerID[:12]},
		{name: "nonhex ID", stdout: strings.Repeat("g", 64)},
		{name: "stderr cannot supply ID", stderr: containerID},
		{name: "failed create cannot supply ID", stdout: containerID, stderr: "synthetic-private-diagnostic", exitCode: "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestQualificationContainerIDProcess$")
			command.Env = append(os.Environ(), "LOOMPLANE_QUALIFY_ID_PROCESS=1", "LOOMPLANE_QUALIFY_ID_STDOUT="+tc.stdout, "LOOMPLANE_QUALIFY_ID_STDERR="+tc.stderr, "LOOMPLANE_QUALIFY_ID_EXIT="+tc.exitCode)
			actual, warned, callErr := qualificationContainerID(command)
			if (callErr == nil) != tc.wantID || warned != tc.wantWarning {
				t.Fatalf("unexpected ID admission or warning classification: accepted=%t warned=%t", callErr == nil, warned)
			}
			if tc.wantID && actual != containerID || !tc.wantID && actual != "" {
				t.Fatal("incorrect container identity returned")
			}
			if callErr != nil && strings.Contains(callErr.Error(), "synthetic-private-diagnostic") {
				t.Fatal("create error exposed command diagnostics")
			}
		})
	}
}

func TestQualificationContainerIDProcess(_ *testing.T) {
	if os.Getenv("LOOMPLANE_QUALIFY_ID_PROCESS") != "1" {
		return
	}
	_, _ = fmt.Fprint(os.Stdout, os.Getenv("LOOMPLANE_QUALIFY_ID_STDOUT"))
	_, _ = fmt.Fprint(os.Stderr, os.Getenv("LOOMPLANE_QUALIFY_ID_STDERR"))
	if os.Getenv("LOOMPLANE_QUALIFY_ID_EXIT") == "1" {
		os.Exit(1)
	}
	os.Exit(0)
}

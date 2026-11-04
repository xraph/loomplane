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
			for _, kind := range []string{"container", "volume"} {
				args := []string{kind, "ls", "-q", "--filter", "label=" + owned.LabelEnvironment + "=" + env}
				raw, e := exec.Command("docker", append([]string{"--host", "unix://" + socket}, args...)...).Output()
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
					if e = exec.Command("docker", append([]string{"--host", "unix://" + socket}, remove...)...).Run(); e != nil {
						t.Error(e)
					}
				}
			}
			for _, network := range []string{server.NetworkName("org", env), server.NetworkName("org", env+"/recovery/"+operation.String())} {
				_ = exec.Command("docker", "--host", "unix://"+socket, "network", "rm", network).Run()
			}
		}()
		if binding.ExternalID != "" {
			if err := target.DeleteRecovery(cleanup, principal, binding, operation.String()); err != nil {
				t.Error(err)
			}
			if err := backend.Remove(cleanup, sourceID, labels); err != nil {
				t.Error(err)
			}
			for _, args := range [][]string{{"volume", "rm", server.VolumeName(sourceID, "data")}, {"network", "rm", server.NetworkName("org", env)}, {"network", "rm", server.NetworkName("org", env+"/recovery/"+operation.String())}} {
				_ = exec.Command("docker", append([]string{"--host", "unix://" + socket}, args...)...).Run()
			}
		}
	})
	source, err := target.Ctrl.Instances.Create(ctx, instance.CreateRequest{Name: "task5-source-" + strings.TrimPrefix(env, "recovery-"), ProviderName: provider, Services: []cpprovider.ServiceSpec{spec}, Labels: labels})
	if err != nil {
		t.Fatal(err)
	}
	sourceID = source.ID
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
		if err := json.Unmarshal(raw, &indices); err != nil {
			t.Fatal(err)
		}
		created, err := strconv.ParseInt(indices[".opensearch-sap-log-types-config"].Settings["index.creation_date"], 10, 64)
		if err != nil || created <= 0 {
			t.Fatal("plugin configuration creation date is invalid")
		}
		return created
	}
	searchCount := func(raw []byte) int64 {
		t.Helper()
		var result struct {
			Count *int64 `json:"count"`
		}
		if err := json.Unmarshal(raw, &result); err != nil || result.Count == nil {
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
			out, err := docker("", "exec", cloneName, "sh", "-c", `sed '/^url = /d' "$1" | curl --silent --show-error --fail --config - --request GET --url "$2"`, "loomplane-qualification", "/usr/share/"+selected+"/config/loomplane-probe.conf", scheme+"://localhost:9200"+endpoint)
			if err != nil {
				t.Fatalf("clone admin query failed: %v", err)
			}
			return out
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

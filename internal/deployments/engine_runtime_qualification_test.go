//go:build qualification

package deployments

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/engineprobe"
	"github.com/xraph/loomplane/internal/enginetls"
	"github.com/xraph/loomplane/internal/recipes"
)

// TestQualifyAuthenticatedEngineRuntimes runs sequentially with private Docker
// networks. DuckDB pins must name actual published qualification manifests.
func TestQualifyAuthenticatedEngineRuntimes(t *testing.T) {
	for _, engine := range []string{"falkordb", "elasticsearch", "opensearch", "duckdb"} {
		t.Run(engine, func(t *testing.T) {
			if selected := os.Getenv("LOOMPLANE_ENGINE"); selected != "" && selected != engine {
				t.Skip("another engine selected")
			}
			kind, ok := recipes.Default().Kind(engine)
			if engine == "duckdb" {
				repository := os.Getenv("LOOMPLANE_DUCKDB_REPOSITORY")
				amd, arm := os.Getenv("LOOMPLANE_DUCKDB_AMD64"), os.Getenv("LOOMPLANE_DUCKDB_ARM64")
				if repository == "" || amd == "" || arm == "" {
					t.Skip("DuckDB qualification requires real registry manifest pins")
				}
				kind = recipes.DuckDBKind(recipes.Image{Repository: repository, Digests: map[string]string{"amd64": amd, "arm64": arm}})
				ok = true
			}
			if !ok {
				t.Fatal("missing engine definition")
			}
			registry := recipes.NewRegistry()
			if err := registry.Register(kind); err != nil {
				t.Fatal(err)
			}
			kind, _ = registry.Kind(engine)
			architecture := runtime.GOARCH
			if selected := os.Getenv("LOOMPLANE_ENGINE_ARCH"); selected != "" {
				if selected != "arm64" && selected != "amd64" {
					t.Fatal("unsupported qualification architecture")
				}
				architecture = selected
			}
			composition, err := registry.Resolve(engine, "", architecture, "server")
			if err != nil {
				t.Fatal(err)
			}
			f := newComponentFixture(t, registry)
			job := f.graphJobForEngine(t, engine, composition.Version, composition)
			job.Action.Resource.TargetSettings["architecture"] = architecture
			run, _, err := f.target.componentEnv(t.Context(), job, kind)
			if err != nil {
				t.Fatal(err)
			}
			files, err := f.target.renderFiles(t.Context(), kind, run)
			if err != nil {
				t.Fatal(err)
			}
			iid := id.New(id.PrefixInstance)
			name := server.ContainerName(iid)
			network := name + "-net"
			volume := name + "-data"
			ref := "org/env/search/runtime_qualification"
			envJSON, err := json.Marshal(run.Env)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.secrets.Put(t.Context(), ref, envJSON); err != nil {
				t.Fatal(err)
			}
			if err = f.target.keepRuntimeFiles(t.Context(), ref, files); err != nil {
				t.Fatal(err)
			}
			labels := map[string]string{owned.LabelOrg: "org", owned.LabelEnvironment: "env", owned.LabelService: "search", owned.LabelRuntime: ref, owned.LabelRuntimeFiles: "true", owned.LabelCatalogResource: "search", owned.LabelCatalogKind: engine, owned.LabelCatalogComponent: engine, owned.LabelHealthChecker: engine + "-v1"}
			values, err := f.target.values(owned.WithRuntimeIdentity(t.Context(), owned.RuntimeIdentity{Instance: iid, Provider: "server"}), owned.InstanceLabels(labels, iid))
			if err != nil {
				t.Fatal(err)
			}
			docker := func(input string, args ...string) ([]byte, error) {
				cmd := exec.CommandContext(t.Context(), "docker", args...)
				cmd.Stdin = strings.NewReader(input)
				return cmd.CombinedOutput()
			}
			must := func(input string, args ...string) []byte {
				out, e := docker(input, args...)
				if e != nil {
					for _, secret := range run.Secrets {
						out = bytes.ReplaceAll(out, secret, []byte("[redacted]"))
					}
					t.Fatalf("Docker operation %s failed: %v: %s", args[0], e, out)
				}
				return out
			}
			t.Cleanup(func() {
				cleanup, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancelCleanup()
				for _, args := range [][]string{{"rm", "-f", name}, {"volume", "rm", volume}, {"network", "rm", network}} {
					_ = exec.CommandContext(cleanup, "docker", args...).Run()
				}
			})
			must("", "network", "create", "--internal", "--label", "loomplane.qualification=standalone-fabriq-engines", network)
			must("", "volume", "create", "--label", "loomplane.qualification=standalone-fabriq-engines", volume)
			directory := t.TempDir()
			envFile := filepath.Join(directory, "env")
			var env strings.Builder
			for key, value := range run.Env {
				fmt.Fprintf(&env, "%s=%s\n", key, value)
			}
			if err = os.WriteFile(envFile, []byte(env.String()), 0o600); err != nil {
				t.Fatal(err)
			}
			component := composition.Components[0]
			args := []string{"run", "-d", "--platform", "linux/" + architecture, "--name", name, "--network", network, "--memory", "2g", "--label", "loomplane.qualification=standalone-fabriq-engines", "--env-file", envFile, "--mount", "type=volume,src=" + volume + ",dst=" + component.Volume.MountPath}
			paths := map[string]string{}
			for path, data := range values.Files {
				local := filepath.Join(directory, fmt.Sprintf("file-%d", len(paths)))
				if err = os.WriteFile(local, data, values.FileMode(path)); err != nil {
					t.Fatal(err)
				}
				paths[path] = local
				args = append(args, "--mount", "type=bind,src="+local+",dst="+path+",readonly")
			}
			if len(component.Command) > 0 {
				args = append(args, "--entrypoint", component.Command[0])
			}
			args = append(args, component.Image)
			if len(component.Command) > 1 {
				args = append(args, component.Command[1:]...)
			}
			args = append(args, component.Args...)
			must("", args...)
			command, err := engineprobe.Command(engine, component.Readiness.Checker)
			if err != nil {
				t.Fatal(err)
			}
			check := func() error { _, e := docker("", append([]string{"exec", name}, command...)...); return e }
			ready := func() {
				deadline := time.Now().Add(3 * time.Minute)
				for {
					if check() == nil {
						return
					}
					if time.Now().After(deadline) {
						logs, _ := docker("", "logs", "--tail", "60", name)
						for _, secret := range run.Secrets {
							logs = bytes.ReplaceAll(logs, secret, []byte("[redacted]"))
						}
						t.Fatalf("authenticated readiness timed out: %s", logs)
					}
					time.Sleep(time.Second)
				}
			}
			ready()
			t.Log("generated runtime files passed authenticated readiness")
			password := string(run.Secrets["password"])
			queryGraph := func(query string) []byte {
				return must("", "exec", name, "sh", "-c", `REDISCLI_AUTH=$(cat /etc/loomplane/password); export REDISCLI_AUTH; exec redis-cli --no-auth-warning --raw GRAPH.QUERY lifecycle "$1"`, "sh", query)
			}
			queryDuck := func(sql string) []byte {
				input := "SET extension_directories=['/opt/duckdb/extensions']; LOAD httpfs; LOAD quack; SET enable_server_cert_verification=true; SET ca_cert_file='/etc/loomplane/ca.pem'; ATTACH 'quack:localhost:9494' AS remote (TOKEN '" + string(run.Secrets["token"]) + "', DISABLE_SSL false); CONNECT remote;\n" + sql + "\n.quit\n"
				return must(input, "exec", "-i", name, "/duckdb", "-batch", "-bail", "-csv", "-noheader")
			}
			querySearch := func(method, path, body string) []byte {
				scheme, user := "http", "elastic"
				ca := ""
				if engine == "opensearch" {
					scheme, user = "https", "loomplane"
					ca = "cacert = \"/usr/share/opensearch/config/ca.pem\"\n"
				}
				config := fmt.Sprintf("url = %q\nuser = %q\nmax-time = 15\n%s", scheme+"://localhost:9200"+path, user+":"+password, ca)
				argv := []string{"exec", "-i", name, "curl", "--fail", "--silent", "--config", "-", "--request", method}
				if body != "" {
					argv = append(argv, "--header", "Content-Type: application/json", "--data-binary", body)
				}
				return must(config, argv...)
			}
			switch engine {
			case "falkordb":
				queryGraph(`CREATE (:Probe {id:1,value:"first"}),(:Probe {id:2,value:"discard"})`)
				queryGraph(`MATCH(n:Probe {id:1}) SET n.value="persisted"`)
				queryGraph(`MATCH(n:Probe {id:2}) DELETE n`)
			case "duckdb":
				queryDuck("CREATE TABLE lifecycle(id INTEGER, value VARCHAR); INSERT INTO lifecycle VALUES(1,'first'),(2,'discard'); UPDATE lifecycle SET value='persisted' WHERE id=1; DELETE FROM lifecycle WHERE id=2;")
			default:
				querySearch("PUT", "/lifecycle", `{"settings":{"number_of_replicas":0}}`)
				querySearch("PUT", "/lifecycle/_doc/1?refresh=true", `{"value":"first"}`)
				querySearch("POST", "/lifecycle/_update/1?refresh=true", `{"doc":{"value":"persisted"}}`)
				querySearch("PUT", "/lifecycle/_doc/2?refresh=true", `{"value":"discard"}`)
				querySearch("DELETE", "/lifecycle/_doc/2?refresh=true", "")
			}
			verify := func() {
				var out []byte
				switch engine {
				case "falkordb":
					out = queryGraph("MATCH(n:Probe) RETURN n.value")
				case "duckdb":
					out = queryDuck("SELECT value FROM lifecycle;")
				default:
					out = querySearch("GET", "/lifecycle/_doc/1", "")
				}
				if !bytes.Contains(out, []byte("persisted")) {
					t.Fatal("stored mutation missing")
				}
			}
			verify()
			credentialPath := "/etc/loomplane/password"
			credential := password
			if engine == "duckdb" {
				credentialPath = "/etc/loomplane/token"
				credential = string(run.Secrets["token"])
			}
			if engine == "elasticsearch" || engine == "opensearch" {
				credentialPath = "/usr/share/" + engine + "/config/loomplane-probe.conf"
			}
			local := paths[credentialPath]
			saved, err := os.ReadFile(local)
			if err != nil {
				t.Fatal(err)
			}
			replace := func(path string, data []byte) {
				if err := os.Chmod(path, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o444); err != nil {
					t.Fatal(err)
				}
			}
			replace(local, bytes.ReplaceAll(saved, []byte(credential), []byte(strings.Repeat("0", 64))))
			rejected := check() != nil
			replace(local, saved)
			if !rejected {
				t.Fatal("wrong credential passed recurring check")
			}
			if slices.Contains([]string{"opensearch", "duckdb"}, engine) {
				caPath := "/etc/loomplane/ca.pem"
				if engine == "opensearch" {
					caPath = "/usr/share/opensearch/config/ca.pem"
				}
				wrongRaw, e := enginetls.Generate(enginetls.Identity{Org: "wrong", Environment: "env", Resource: "search", Kind: engine}, "", time.Now())
				if e != nil {
					t.Fatal(e)
				}
				wrong, e := enginetls.Parse(wrongRaw, enginetls.Identity{Org: "wrong", Environment: "env", Resource: "search", Kind: engine}, time.Now())
				if e != nil {
					t.Fatal(e)
				}
				local = paths[caPath]
				saved, e = os.ReadFile(local)
				if e != nil {
					t.Fatal(e)
				}
				replace(local, wrong.CACert)
				rejected = check() != nil
				replace(local, saved)
				if !rejected {
					t.Fatal("wrong CA passed recurring check")
				}
			}
			ready()
			if engine == "duckdb" {
				stopStarted := time.Now()
				must("", "stop", "--time", "30", name)
				if strings.TrimSpace(string(must("", "inspect", "--format", "{{.State.ExitCode}}", name))) != "0" {
					t.Fatal("DuckDB did not shut down cleanly")
				}
				t.Logf("DuckDB TERM shutdown exited 0 in %s", time.Since(stopStarted))
				must("", "start", name)
			} else {
				must("", "restart", "--time", "30", name)
			}
			ready()
			verify()
			logs := must("", "logs", name)
			for _, secret := range []string{password, string(run.Secrets["token"])} {
				if secret != "" && bytes.Contains(logs, []byte(secret)) {
					t.Fatal("runtime logged a credential")
				}
			}
			t.Log("authenticated CRUD, wrong credentials/trust and restart persistence passed")
		})
	}
}

//go:build qualification

package deployments

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/engineprobe"
	"github.com/xraph/loomplane/internal/recipes"
)

// TestQualifyEngineKubernetes requires a dedicated disposable cluster. The
// kubeconfig and retained storage class must be explicitly selected by its owner.
func TestQualifyEngineKubernetes(t *testing.T) {
	configPath := os.Getenv("LOOMPLANE_ENGINE_KUBECONFIG")
	if configPath == "" {
		t.Skip("explicit isolated kubeconfig required")
	}
	rc, err := clientcmd.BuildConfigFromFlags("", configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, engine := range []string{"falkordb", "elasticsearch", "opensearch", "duckdb"} {
		t.Run(engine, func(t *testing.T) {
			if selected := os.Getenv("LOOMPLANE_ENGINE"); selected != "" && selected != engine {
				t.Skip("another engine selected")
			}
			namespace := "lp-engine-" + engine
			client, err := kubernetes.New(&kubernetes.Config{REST: rc, Namespace: namespace})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = client.Kube.CoreV1().Namespaces().Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = client.Kube.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
			})
			class, err := client.Kube.StorageV1().StorageClasses().Get(t.Context(), "lp-engine-retain", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			client.Config.Details["storage_class"], client.Config.Details["storage_class_uid"] = class.Name, string(class.UID)
			kind, ok := recipes.Default().Kind(engine)
			if engine == "duckdb" {
				repository := os.Getenv("LOOMPLANE_DUCKDB_REPOSITORY")
				amd, arm := os.Getenv("LOOMPLANE_DUCKDB_AMD64"), os.Getenv("LOOMPLANE_DUCKDB_ARM64")
				if repository == "" || amd == "" || arm == "" {
					t.Fatal("DuckDB requires real registry manifest pins")
				}
				kind = recipes.DuckDBKind(recipes.Image{Repository: repository, Digests: map[string]string{"amd64": amd, "arm64": arm}})
				ok = true
			}
			if !ok {
				t.Fatal("missing engine")
			}
			registry := recipes.NewRegistry()
			if err = registry.Register(kind); err != nil {
				t.Fatal(err)
			}
			kind, _ = registry.Kind(engine)
			composition, err := registry.Resolve(engine, "", runtime.GOARCH, "kubernetes")
			if err != nil {
				t.Fatal(err)
			}
			fixture := newComponentFixture(t, registry)
			fixture.target.Cloud = "kubernetes"
			job := fixture.graphJobForEngine(t, engine, composition.Version, composition)
			job.Action.Resource.Provider = "kubernetes"
			job.Action.Resource.TargetSettings = map[string]string{"namespace": namespace, "architecture": runtime.GOARCH}
			run, _, err := fixture.target.componentEnv(t.Context(), job, kind)
			if err != nil {
				t.Fatal(err)
			}
			files, err := fixture.target.renderFiles(t.Context(), kind, run)
			if err != nil {
				t.Fatal(err)
			}
			ref := "org/env/search/runtime_kubernetes"
			envJSON, err := json.Marshal(run.Env)
			if err != nil {
				t.Fatal(err)
			}
			if err = fixture.secrets.Put(t.Context(), ref, envJSON); err != nil {
				t.Fatal(err)
			}
			if err = fixture.target.keepRuntimeFiles(t.Context(), ref, files); err != nil {
				t.Fatal(err)
			}
			labels := map[string]string{owned.LabelOrg: "org", owned.LabelProject: "proj", owned.LabelEnvironment: "env", owned.LabelConnection: "conn", owned.LabelService: "search", owned.LabelRuntime: ref, owned.LabelRuntimeFiles: "true", owned.LabelPublic: "false", owned.LabelRevision: "qualification", owned.LabelStartTimeout: "240"}
			component := composition.Components[0]
			spec := cpprovider.ServiceSpec{Name: engine, Image: component.Image, Env: run.Env}
			componentSpec(&spec, labels, job.Action.Resource.Component, "kubernetes")
			iid := id.New(id.PrefixInstance)
			name := kubernetes.WorkloadName(iid)
			pod := name + "-0"
			backend := &kubernetes.Runtime{Session: func(context.Context, map[string]string) (*kubernetes.Client, error) { return client, nil }, Values: fixture.target.values, Wait: 4 * time.Minute}
			if _, err = backend.Apply(t.Context(), iid, spec, labels); err != nil {
				t.Fatal(err)
			}
			command, err := engineprobe.Command(engine, component.Readiness.Checker)
			if err != nil {
				t.Fatal(err)
			}
			kube := func(input string, args ...string) ([]byte, error) {
				cmd := exec.CommandContext(t.Context(), "kubectl", append([]string{"--kubeconfig", configPath, "-n", namespace}, args...)...)
				cmd.Stdin = strings.NewReader(input)
				return cmd.CombinedOutput()
			}
			execute := func(input string, args ...string) []byte {
				out, e := kube(input, append([]string{"exec", "-i", pod, "--"}, args...)...)
				if e != nil {
					t.Fatalf("engine command failed: %v", e)
				}
				return out
			}
			execute("", command...)
			actual, err := client.Kube.CoreV1().Pods(namespace).Get(t.Context(), pod, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if actual.Spec.Containers[0].ReadinessProbe.Exec == nil {
				t.Fatal("authenticated exec probe missing")
			}
			query := func(write bool) []byte {
				switch engine {
				case "falkordb":
					sql := "MATCH(n:Probe) RETURN n.value"
					if write {
						sql = `CREATE (:Probe {value:"persisted"})`
					}
					return execute("", "sh", "-c", `REDISCLI_AUTH=$(cat /etc/loomplane/password); export REDISCLI_AUTH; exec redis-cli --no-auth-warning --raw GRAPH.QUERY lifecycle "$1"`, "sh", sql)
				case "duckdb":
					sql := "SELECT value FROM lifecycle;"
					if write {
						sql = "CREATE TABLE lifecycle(value VARCHAR); INSERT INTO lifecycle VALUES ('persisted');"
					}
					input := "SET extension_directories=['/opt/duckdb/extensions']; LOAD httpfs; LOAD quack; SET enable_server_cert_verification=true; SET ca_cert_file='/etc/loomplane/ca.pem'; ATTACH 'quack:" + name + "." + namespace + ".svc:9494' AS remote (TOKEN '" + string(run.Secrets["token"]) + "', DISABLE_SSL false); CONNECT remote;\n" + sql + "\n.quit\n"
					return execute(input, "/duckdb", "-batch", "-bail", "-csv", "-noheader")
				default:
					scheme, user, ca := "http", "elastic", ""
					if engine == "opensearch" {
						scheme, user, ca = "https", "loomplane", "cacert = \"/usr/share/opensearch/config/ca.pem\"\n"
					}
					config := fmt.Sprintf("url = %q\nuser = %q\nmax-time = 15\n%s", scheme+"://"+name+"."+namespace+".svc:9200/lifecycle/_doc/1", user+":"+string(run.Secrets["password"]), ca)
					args := []string{"curl", "--fail", "--silent", "--config", "-"}
					if write {
						args = append(args, "-X", "PUT", "-H", "Content-Type: application/json", "--data-binary", `{"value":"persisted"}`)
					}
					return execute(config, args...)
				}
			}
			query(true)
			if !bytes.Contains(query(false), []byte("persisted")) {
				t.Fatal("write not readable")
			}
			if _, err = kube("", "delete", "pod", pod, "--wait=true"); err != nil {
				t.Fatal(err)
			}
			if _, err = kube("", "wait", "--for=condition=Ready", "pod/"+pod, "--timeout=240s"); err != nil {
				t.Fatal(err)
			}
			execute("", command...)
			if !bytes.Contains(query(false), []byte("persisted")) {
				t.Fatal("persistent data missing after pod replacement")
			}
			t.Log("actual StatefulSet, Secret file modes, recurring exec readiness, service DNS TLS and retained-volume pod replacement passed")
			if err = backend.Remove(t.Context(), iid, labels); err != nil {
				t.Fatal(err)
			}
		})
	}
}

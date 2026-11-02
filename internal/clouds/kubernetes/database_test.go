package kubernetes

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/owned"
	nativepg "github.com/xraph/loomplane/internal/clouds/postgres"
	nativecache "github.com/xraph/loomplane/internal/clouds/redis"
)

func TestDatabaseWorkflowPortRefusesUnqualifiedEngineBeforeAccess(t *testing.T) {
	r := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
		t.Fatal("opened an unqualified engine target")
		return nil, nil
	}}
	var native owned.DatabaseBackend = r
	labels := map[string]string{owned.LabelStateful: "true", owned.LabelPublic: "false", nativepg.LabelRecovery: "restore-fixture", "loomplane.mysql.database": "database", "loomplane.mysql.engine": "mysql"}
	iid := id.New(id.PrefixInstance)
	if err := native.ConfigureDatabase(t.Context(), iid, labels, "fixture-application-password-for-engine"); err == nil {
		t.Fatal("configured an unqualified engine")
	}
	if _, err := native.BackupDatabase(t.Context(), iid, labels, nil, "key"); err == nil {
		t.Fatal("exported an unqualified engine")
	}
	if err := native.RestoreDatabase(t.Context(), iid, labels, nil, backupfiles.Object{}); err == nil {
		t.Fatal("restored an unqualified engine")
	}
	if err := native.DatabaseProbe(t.Context(), iid, labels); err == nil {
		t.Fatal("probed an unqualified engine")
	}
}

// cacheFixture applies a Redis StatefulSet and starts its pod, as the
// cluster would.
func cacheFixture(t *testing.T) (*Runtime, *Client, id.ID, map[string]string, *[]string) {
	t.Helper()
	r, c, labels, sp := persistentFixture()
	sessions := []string{}
	session := r.Session
	r.Session = func(ctx context.Context, labels map[string]string) (*Client, error) {
		sessions = append(sessions, labels[owned.LabelOperation])
		return session(ctx, labels)
	}
	sp.Name, sp.Image = "cache", "redis@sha256:reviewed"
	sp.Ports = []cpprovider.PortSpec{{Container: 6379, Protocol: "tcp"}}
	labels[owned.LabelPublic] = "false"
	labels[owned.LabelImage] = sp.Image
	labels[nativecache.LabelEngine] = "redis"
	iid := id.New(id.PrefixInstance)
	if _, err := r.Apply(t.Context(), iid, sp, labels); err != nil {
		t.Fatal(err)
	}
	set, err := c.Kube.AppsV1().StatefulSets("selected").Get(t.Context(), WorkloadName(iid), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	set.UID = "set-identity"
	if set, err = c.Kube.AppsV1().StatefulSets("selected").Update(t.Context(), set, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	controller := true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: set.Name + "-0", Namespace: "selected", UID: "pod-identity", Labels: set.Spec.Template.Labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: set.Name, UID: set.UID, Controller: &controller}}}, Spec: set.Spec.Template.Spec, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if _, err = c.Kube.CoreV1().Pods("selected").Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	sessions = sessions[:0]
	return r, c, iid, labels, &sessions
}

func TestKubernetesConfigureCacheDispatchesByEngine(t *testing.T) {
	r, _, iid, labels, sessions := cacheFixture(t)
	password := "fixture-application-password-for-cache"
	var commands [][]string
	uploaded := map[string]string{}
	exec := cacheExec
	t.Cleanup(func() { cacheExec = exec })
	cacheExec = func(_ context.Context, _ *Client, pod *corev1.Pod, args []string, input io.Reader, out io.Writer, title string) error {
		if pod.Name != WorkloadName(iid)+"-0" || pod.UID != "pod-identity" || title != "Redis" {
			t.Errorf("exec in pod %s %s", pod.Name, pod.UID)
		}
		commands = append(commands, args)
		switch {
		case slices.Equal(args, []string{"tar", "-xf", "-", "-C", "/tmp"}):
			archive := tar.NewReader(input)
			for {
				header, err := archive.Next()
				if errors.Is(err, io.EOF) {
					return nil
				}
				if err != nil {
					return err
				}
				data, _ := io.ReadAll(archive)
				uploaded[header.Name] = string(data)
			}
		case args[len(args)-1] == "PING":
			_, err := io.WriteString(out, "PONG\n")
			return err
		}
		return nil
	}
	if err := r.ConfigureDatabase(t.Context(), iid, labels, password); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*sessions, []string{"read"}) {
		t.Fatalf("sessions: %v", *sessions)
	}
	if len(uploaded) != 1 {
		t.Fatalf("uploaded %v", uploaded)
	}
	var name string
	for key, value := range uploaded {
		name = key
		if value != password {
			t.Fatal("uploaded another password")
		}
	}
	ping := slices.IndexFunc(commands, func(args []string) bool { return args[len(args)-1] == "PING" })
	if ping < 0 || !slices.Contains(commands[ping], "/tmp/"+name) || !slices.Contains(commands[ping], "redis-cli") || !slices.Contains(commands[ping], "app") {
		t.Fatalf("commands: %q", commands)
	}
	if last := commands[len(commands)-1]; !slices.Equal(last, []string{"rm", "-f", "/tmp/" + name}) {
		t.Fatalf("password file kept: %q", commands)
	}
	for _, args := range commands {
		if strings.Contains(strings.Join(args, " "), password) {
			t.Fatal("the password entered a command line")
		}
	}

	// A foreign engine label on the workload refuses before exec.
	commands = nil
	other := map[string]string{}
	for key, value := range labels {
		other[key] = value
	}
	other[nativecache.LabelEngine] = "valkey"
	if err := r.ConfigureDatabase(t.Context(), iid, other, password); err == nil || len(commands) > 0 {
		t.Fatalf("configured a workload of another engine: %v %q", err, commands)
	}

	// PostgreSQL labels still go to ConfigurePostgres, which finds no
	// PostgreSQL workload here.
	*sessions = (*sessions)[:0]
	postgres := map[string]string{}
	for key, value := range labels {
		postgres[key] = value
	}
	delete(postgres, nativecache.LabelEngine)
	postgres[nativepg.LabelDatabase] = "database"
	if err := r.ConfigureDatabase(t.Context(), iid, postgres, password); err == nil || len(commands) > 0 || len(*sessions) != 1 {
		t.Fatalf("postgres dispatch: %v %q %v", err, commands, *sessions)
	}
}

func TestKubernetesCacheBackupsUnavailable(t *testing.T) {
	if ErrCacheBackupsUnavailable.Error() != "backups for Redis and Valkey aren't available on Kubernetes yet" {
		t.Fatalf("message: %q", ErrCacheBackupsUnavailable)
	}
	r := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
		t.Fatal("opened a session for a cache backup")
		return nil, nil
	}}
	iid := id.New(id.PrefixInstance)
	labels := map[string]string{owned.LabelStateful: "true", owned.LabelPublic: "false", owned.LabelImage: "redis@sha256:reviewed", nativecache.LabelEngine: "valkey"}
	if _, err := r.BackupDatabase(t.Context(), iid, labels, nil, "key"); !errors.Is(err, ErrCacheBackupsUnavailable) {
		t.Fatalf("backup: %v", err)
	}
	if err := r.RestoreDatabase(t.Context(), iid, labels, nil, backupfiles.Object{}); !errors.Is(err, ErrCacheBackupsUnavailable) {
		t.Fatalf("restore: %v", err)
	}
	if err := r.DatabaseProbe(t.Context(), iid, labels); !errors.Is(err, ErrCacheBackupsUnavailable) {
		t.Fatalf("probe: %v", err)
	}
}

func TestKubernetesPostgresExecErrorsNameTheEngine(t *testing.T) {
	pod := &corev1.Pod{TypeMeta: metav1.TypeMeta{Kind: "Pod", APIVersion: "v1"}, ObjectMeta: metav1.ObjectMeta{Name: "fixture-0", Namespace: "selected", UID: "pod-identity"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "database", Image: "postgres@sha256:reviewed", Env: []corev1.EnvVar{podIdentityEnv()}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces/selected/pods/fixture-0":
			_ = json.NewEncoder(w).Encode(pod)
		case r.URL.Path == "/api/v1/namespaces/selected/pods/fixture-0/exec":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	cfg := &rest.Config{Host: api.URL}
	kube, err := kubeclient.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := &postgresPod{client: &Client{Config: &Config{Namespace: "selected", REST: cfg}, Kube: kube}, pod: pod}
	if err = s.run(t.Context(), []string{"true"}, io.Discard); err == nil || !strings.HasPrefix(err.Error(), "native PostgreSQL command failed: ") {
		t.Fatalf("run: %v", err)
	}
	if err = s.upload(t.Context(), "nested/name", 0, nil); err == nil || err.Error() != "invalid PostgreSQL temporary file" {
		t.Fatalf("upload: %v", err)
	}
	cache := (&cachePod{client: s.client, pod: pod, engine: "valkey"}).database()
	if err = cache.Run(t.Context(), []string{"true"}, io.Discard); err == nil || !strings.HasPrefix(err.Error(), "native Valkey command failed: ") {
		t.Fatalf("cache run: %v", err)
	}
	if err = cache.Upload(t.Context(), "nested/name", 0, nil); err == nil || err.Error() != "invalid Valkey temporary file" {
		t.Fatalf("cache upload: %v", err)
	}
}

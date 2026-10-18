//go:build integration

package kubernetes_test

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/trove/driver"

	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/trove"
	"github.com/xraph/trove/drivers/localdriver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	nativepg "github.com/xraph/loomplane/internal/clouds/postgres"
	"github.com/xraph/loomplane/internal/testfixtures/cluster"
)

func TestAcceptanceKubernetesNativePostgresRecovery(t *testing.T) {
	path, runtime, client, labels, spec := cluster.New(t)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	labels[nativepg.LabelDatabase] = "database"
	labels["loomplane.image.ref"] = spec.Image
	source := id.New(id.PrefixInstance)
	if _, err := runtime.Apply(ctx, source, spec, labels); err != nil {
		t.Fatal(err)
	}
	password := "native-fixture-app-password-with-entropy"
	if err := runtime.ConfigurePostgres(ctx, source, labels, password); err != nil {
		t.Fatal(err)
	}
	sql := func(iid id.ID, query string) string {
		return cluster.Command(t, "kubectl", "--kubeconfig", path, "-n", client.Config.Namespace, "exec", kubernetes.WorkloadName(iid)+"-0", "--", "psql", "-U", "postgres", "-d", "database", "-At", "-v", "ON_ERROR_STOP=1", "-c", query)
	}
	if got := sql(source, "SET ROLE app; CREATE TABLE native_probe(value text); INSERT INTO native_probe VALUES ('restored-through-exec'); SELECT value FROM native_probe"); !strings.Contains(got, "restored-through-exec") {
		t.Fatal(got)
	}
	driver := localdriver.New()
	if err := driver.Open(ctx, "file://"+t.TempDir()); err != nil {
		t.Fatal(err)
	}
	files, err := trove.Open(driver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := files.Close(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	})
	if err = files.CreateBucket(ctx, "backups"); err != nil {
		t.Fatal(err)
	}
	store := &backupfiles.Store{Trove: files, Bucket: "backups"}
	object, err := runtime.BackupPostgres(ctx, source, labels, store, "recovery/database.dump")
	if err != nil {
		t.Fatal(err)
	}
	if object.Size == 0 || object.SHA256 == "" {
		t.Fatal("missing native recovery evidence")
	}
	sql(source, "DELETE FROM native_probe")
	if err = runtime.RestorePostgres(ctx, source, labels, store, object); err == nil {
		t.Fatal("restored over the live source")
	}
	restored := id.New(id.PrefixInstance)
	recovery := maps.Clone(labels)
	recovery[nativepg.LabelRecovery] = "restore_fixture"
	result, err := runtime.Apply(ctx, restored, spec, recovery)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Endpoints) != 0 {
		t.Fatalf("recovery copy was published: %+v", result.Endpoints)
	}
	if err = runtime.ConfigurePostgres(ctx, restored, recovery, password); err != nil {
		t.Fatal(err)
	}
	corrupt := object
	corrupt.SHA256 = strings.Repeat("0", 64)
	if err = runtime.RestorePostgres(ctx, restored, recovery, store, corrupt); err == nil {
		t.Fatal("restored corrupt backup bytes")
	}
	if err = runtime.RestorePostgres(ctx, restored, recovery, store, object); err != nil {
		t.Fatal(err)
	}
	if err = runtime.RestoreProbe(ctx, restored, recovery); err != nil {
		t.Fatal(err)
	}
	if got := sql(restored, "SELECT value FROM native_probe"); got != "restored-through-exec" {
		t.Fatal(got)
	}
	if got := sql(source, "SELECT count(*) FROM native_probe"); got != "0" {
		t.Fatalf("live source was changed: %s", got)
	}
	claim, err := client.Kube.CoreV1().PersistentVolumeClaims(client.Config.Namespace).Get(ctx, owned.PersistentVolumeName(restored, "data"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	volume := claim.Spec.VolumeName
	if err = runtime.PurgeRecovery(ctx, restored, recovery); err != nil {
		t.Fatal(err)
	}
	if err = runtime.PurgeRecovery(ctx, restored, recovery); err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
	if _, err = client.Kube.CoreV1().PersistentVolumes().Get(ctx, volume, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("temporary volume survived cleanup: %v", err)
	}
	if _, err = client.Kube.CoreV1().PersistentVolumeClaims(client.Config.Namespace).Get(ctx, owned.PersistentVolumeName(source, "data"), metav1.GetOptions{}); err != nil {
		t.Fatalf("cleanup touched source data: %v", err)
	}
	var download bytes.Buffer
	if err = store.CopyVerified(ctx, object, &download); err != nil || int64(download.Len()) != object.Size {
		t.Fatalf("cleanup touched recovery object: %v", err)
	}
	t.Logf("native SQL role, Trove export, enforced ingress/egress isolation, corrupt-file rejection and fresh restore verified; temporary PV %s removed", volume)
}

// This opt-in run uses a disposable CNI with enforcement deliberately disabled.
// A NetworkPolicy manifest must never substitute for successful denial probes.
func TestAcceptanceKubernetesRejectsUnenforcedRecovery(t *testing.T) {
	if os.Getenv("LOOMPLANE_TEST_NETWORK_POLICY_DISABLED") != "1" {
		t.Skip("set LOOMPLANE_TEST_NETWORK_POLICY_DISABLED=1 only on the disposable unenforced target")
	}
	_, runtime, _, labels, spec := cluster.New(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	labels[nativepg.LabelDatabase] = "database"
	labels[nativepg.LabelRecovery] = "restore-unenforced"
	labels["loomplane.image.ref"] = spec.Image
	iid := id.New(id.PrefixInstance)
	if _, err := runtime.Apply(ctx, iid, spec, labels); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ConfigurePostgres(ctx, iid, labels, "fixture-password-for-unenforced-copy"); err != nil {
		t.Fatal(err)
	}
	drv := localdriver.New()
	if openErr := drv.Open(ctx, "file://"+t.TempDir()); openErr != nil {
		t.Fatal(openErr)
	}
	reads := &recoveryReadGuard{Driver: drv}
	files, openErr := trove.Open(reads)
	if openErr != nil {
		t.Fatal(openErr)
	}
	t.Cleanup(func() {
		if closeErr := files.Close(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	})
	if bucketErr := files.CreateBucket(ctx, "backups"); bucketErr != nil {
		t.Fatal(bucketErr)
	}
	store := &backupfiles.Store{Trove: files, Bucket: "backups"}
	err := runtime.RestorePostgres(ctx, iid, labels, store, backupfiles.Object{Key: "must-not-be-read", Size: 1, SHA256: strings.Repeat("0", 64)})
	if reads.reads.Load() != 0 {
		t.Fatal("backup object was read before isolation was established")
	}

	if !errors.Is(err, kubernetes.ErrIsolation) {
		t.Fatalf("unenforced recovery did not fail before file access: %v", err)
	}
	if err = runtime.PurgeRecovery(ctx, iid, labels); err != nil {
		t.Fatal(err)
	}
	t.Log("unenforced ingress/egress was rejected before any backup object was read")
}

// The real Trove local driver owns lifecycle; this boundary records any attempt
// to fetch a recovery object before the network gate permits it.
type recoveryReadGuard struct {
	driver.Driver
	reads atomic.Int32
}

func (g *recoveryReadGuard) Get(context.Context, string, string, ...driver.GetOption) (*driver.ObjectReader, error) {
	g.reads.Add(1)
	return nil, errors.New("recovery object read before isolation")
}

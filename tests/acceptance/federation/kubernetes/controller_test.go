package kubernetes_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	"github.com/xraph/grove/drivers/pgdriver"
	"k8s.io/client-go/kubernetes"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func proveControllerReceipts(ctx context.Context, t *testing.T, db *pgdriver.PgDB, client kubernetes.Interface, d domain.Destination) {
	t.Helper()
	_, err := db.Exec(ctx, `UPDATE loomplane_cloud_connections SET details=jsonb_build_object('namespace','apps','namespace_uid',$1::text,'cluster_ca',$2::text) WHERE org_id=$3 AND id=$4`, d.Identity.NamespaceUID, d.Identity.Account, d.OrgID, d.ConnectionID)
	must(t, err)
	iid := id.New(id.PrefixInstance)
	store := kube.NewControllerStore(db)
	target := &kube.Client{Config: &kube.Config{Namespace: "apps", Details: map[string]string{"namespace_uid": d.Identity.NamespaceUID, "cluster_ca": d.Identity.Account}}, Kube: client}
	runtime := &kube.Runtime{Session: func(context.Context, map[string]string) (*kube.Client, error) { return target, nil }, RecordController: store.RecordController}
	labels := map[string]string{owned.LabelOrg: d.OrgID, owned.LabelProject: d.ProjectID, owned.LabelEnvironment: d.EnvironmentID, owned.LabelConnection: d.ConnectionID, owned.LabelService: "receipt-probe", owned.LabelPlan: "fixture-plan", owned.LabelRuntime: "fixture-runtime", owned.LabelRevision: "first"}
	spec := cpprovider.ServiceSpec{Name: "main", Image: os.Getenv("AGENT_IMAGE"), Command: []string{"/qualification", "-test.run=^TestControllerReceiptProbe$", "-test.v"}, Env: map[string]string{"CONTROLLER_RECEIPT_PROBE": "1"}}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		must(t, runtime.Remove(cleanup, iid, labels))
	})
	_, err = runtime.Apply(ctx, iid, spec, labels)
	must(t, err)
	first, err := store.GetController(ctx, d.OrgID, d.ConnectionID, iid.String(), labels[owned.LabelRuntime])
	must(t, err)
	labels[owned.LabelRevision] = "retry"
	_, err = runtime.Apply(ctx, iid, spec, labels)
	must(t, err)
	reopened := kube.NewControllerStore(db)
	second, err := reopened.GetController(ctx, d.OrgID, d.ConnectionID, iid.String(), labels[owned.LabelRuntime])
	must(t, err)
	if !first.Valid() || !second.Valid() || second.DeploymentUID != first.DeploymentUID || second.DeploymentGeneration <= first.DeploymentGeneration || second.RuntimeRevision != "retry" {
		t.Fatal("controller update receipt lost", first, second)
	}
	if err = store.RecordController(ctx, *first); err == nil {
		t.Fatal("stale controller mutation overwrote retry")
	}
	t.Log("PASS real runtime saves controller creation and update receipts after readiness; reopened store rejects stale generation")
}

func TestControllerReceiptProbe(t *testing.T) {
	if os.Getenv("CONTROLLER_RECEIPT_PROBE") != "1" {
		t.Skip("runs only in the controller receipt deployment")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	<-ctx.Done()
}

package kubernetes_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/config"
	fed "github.com/xraph/loomplane/internal/federation"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/workloadidentity"
)

// TestPreparedKubernetesIdentity runs only in the disposable cluster created by
// run.py. It uses real SPIRE PSAT, CSI, PostgreSQL and the agent executable.
func TestPreparedKubernetesIdentity(t *testing.T) {
	if os.Getenv("LOOMPLANE_KUBERNETES_QUALIFICATION") != "1" {
		t.Skip("run with the owned kind fixture")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	cfg, err := rest.InClusterConfig()
	must(t, err)
	client, err := kubernetes.NewForConfig(cfg)
	must(t, err)
	own, err := client.CoreV1().Pods("spire").Get(ctx, os.Getenv("POD_NAME"), metav1.GetOptions{})
	must(t, err)
	node, err := client.CoreV1().Nodes().Get(ctx, own.Spec.NodeName, metav1.GetOptions{})
	must(t, err)
	admin := openIdentity(ctx, t, own, string(node.UID))
	db, store, credentials, destination := database(ctx, t, client)
	principal := security.Principal{OrgID: "fixture", UserID: "operator"}
	destinations := fed.NewService(store, credentials, nil, fixtureAuthorizer{}, nil, true)
	enrollments := workloadidentity.NewEnrollmentStore(db, "loomplane.test")
	installation := fed.NewInstallationService(destinations, store, fed.VerifyKubernetesInstallation(credentials, admin, "loomplane.test", "fixture"))
	runtime := startRuntime(ctx, t, db)
	launch := fed.NewLaunchService(installation, enrollments, credentials, runtime.PublicSigningKey, config.FederationLaunchConfig{Enabled: true, ControlEndpoint: "https://control.spire.svc:9443", AgentImage: os.Getenv("AGENT_IMAGE"), TrustGeneration: 1}, "fixture", store)
	preparation := fed.NewPreparationService(launch, store)
	enrollment := fed.NewEnrollmentService(destinations, enrollments, installation.Resolve, admin)
	coordinator := fed.NewPreparationLaunchService(preparation, enrollment)
	prepared, err := preparation.Start(ctx, principal, destination.ID)
	must(t, err)
	name := "loomplane-agent-" + prepared.ID
	wait(ctx, t, "prepared pod scheduled", func() error {
		pod, getErr := client.CoreV1().Pods("apps").Get(ctx, name, metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		if pod.Spec.NodeName == "" {
			return errors.New("not scheduled")
		}
		return nil
	})
	input := fed.PreparedLaunchInput{DestinationID: destination.ID, PreparationID: prepared.ID, SPIRECluster: "qualification", SPIRENamespace: "spire", SPIREServiceAccount: "spire-agent"}
	// Resolve actual node evidence before issuing any workload identity.
	bad := input
	bad.SPIRECluster = "foreign"
	if _, denied := coordinator.Launch(ctx, principal, bad); denied == nil {
		t.Fatal("foreign SPIRE parent accepted")
	}
	var count int
	must(t, db.QueryRow(ctx, `SELECT count(*) FROM loomplane_federation_enrollments`).Scan(&count))
	if count != 0 {
		t.Fatal("parent denial reserved enrollment")
	}
	t.Log("PASS foreign PSAT parent rejected before enrollment")
	delivered, err := coordinator.Launch(ctx, principal, input)
	must(t, err)
	if delivered.PodUID != prepared.PodUID {
		t.Fatal("launch changed prepared pod identity")
	}
	wait(ctx, t, "agent enrollment and signed policy acknowledgment", func() error {
		return acknowledged(ctx, db, delivered.EnrollmentID)
	})
	t.Log("PASS prepared pod launches through CSI, enrolls with PSAT identity and acknowledges signed policy")
	// Reconstruct the coordinator and recover a delivered attempt after the
	// one-use handle was consumed. This must not issue another enrollment.
	restarted := fed.NewPreparationLaunchService(preparation, enrollment)
	recovered, err := restarted.Launch(ctx, principal, input)
	must(t, err)
	if *recovered != *delivered {
		t.Fatal("restart changed launch receipt")
	}
	must(t, db.QueryRow(ctx, `SELECT count(*) FROM loomplane_federation_enrollments`).Scan(&count))
	if count != 1 {
		t.Fatal("recovery allocated another enrollment")
	}
	t.Log("PASS coordinator recovery after bootstrap consumption retains one enrollment")
	// The same account and CSI volume in a neighboring pod cannot acquire the
	// original pod's SVID. A successful mount alone must not satisfy attestation.
	proveNeighborDenied(ctx, t, client, name)
	retirement := fed.NewPreparationRetirementService(preparation, fed.NewLaunchRetirementService(launch, func(ctx context.Context, org, id string) error { return enrollments.Revoke(ctx, org, id, admin) }), func(ctx context.Context, org, dest, id string) error {
		return enrollments.RevokePreparation(ctx, org, dest, id, admin)
	})
	wait(ctx, t, "retirement after signed lease expiry", func() error {
		result, retireErr := retirement.Retire(ctx, principal, destination.ID, prepared.ID)
		if retireErr != nil {
			return retireErr
		}
		if result.State != fed.PreparationRetired {
			return errors.New("retirement incomplete")
		}
		return nil
	})
	_, err = client.CoreV1().Pods("apps").Get(ctx, name, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("owned pod remains: %v", err)
	}
	_, err = client.CoreV1().ServiceAccounts("apps").Get(ctx, name, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("owned account remains: %v", err)
	}
	_, err = client.CoreV1().ConfigMaps("apps").Get(ctx, name, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("owned configuration remains: %v", err)
	}
	t.Log("PASS retirement revokes enrollment and removes exact pod, configuration and account")
}

type fixtureAuthorizer struct{}

func (fixtureAuthorizer) Require(_ context.Context, p security.Principal, _ security.Permission, _ string) error {
	if p.OrgID != "fixture" || p.UserID != "operator" || p.ReadOnly {
		return security.ErrForbidden
	}
	return nil
}

type noWake struct{}

func (noWake) Watch(string) (<-chan struct{}, func()) { return nil, func() {} }
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func wait(ctx context.Context, t *testing.T, label string, check func() error) {
	t.Helper()
	var last error
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if last = check(); last == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s: %v (%v)", label, ctx.Err(), last)
		case <-ticker.C:
		}
	}
}
func acknowledged(ctx context.Context, db *pgdriver.PgDB, id string) error {
	var phase string
	var raw []byte
	if err := db.QueryRow(ctx, `SELECT phase,body FROM loomplane_federation_enrollments WHERE org_id='fixture' AND id=$1`, id).Scan(&phase, &raw); err != nil {
		return err
	}
	if phase != "consumed" {
		return fmt.Errorf("enrollment phase %s", phase)
	}
	var record workloadidentity.EnrollmentRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return err
	}
	state, err := access.NewPolicyStore(db).Delivery(ctx, record.Binding.Agent)
	if err != nil {
		return err
	}
	if state.AcknowledgedSequence == 0 || state.Revision == "" || state.ReceivedAt.IsZero() || !state.ExpiresAt.After(time.Now()) {
		return errors.New("no current acknowledged policy")
	}
	return nil
}

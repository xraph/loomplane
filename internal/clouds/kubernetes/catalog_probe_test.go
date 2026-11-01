package kubernetes

import (
	"reflect"
	"testing"

	"github.com/xraph/ctrlplane/id"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/engineprobe"
)

func TestAuthenticatedCatalogReadinessUsesFixedExec(t *testing.T) {
	for _, kind := range []string{"falkordb", "elasticsearch", "opensearch", "duckdb"} {
		labels := map[string]string{owned.LabelCatalogKind: kind, owned.LabelHealthChecker: kind + "-v1"}
		pod, _, err := runtimePod(cpprovider.ServiceSpec{Name: "engine", Image: "qualified"}, labels, owned.RuntimeValues{}, nil, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		probe := pod.Spec.Containers[0].ReadinessProbe
		command, _ := engineprobe.Command(kind, kind+"-v1")
		if probe == nil || probe.Exec == nil || probe.HTTPGet != nil || probe.TCPSocket != nil || !reflect.DeepEqual(probe.Exec.Command, command) || probe.PeriodSeconds == 0 || probe.TimeoutSeconds != 5 {
			t.Fatalf("%s readiness: %+v", kind, probe)
		}
		labels[owned.LabelHealthChecker] = "unknown"
		if _, _, err = runtimePod(cpprovider.ServiceSpec{Name: "engine"}, labels, owned.RuntimeValues{}, nil, nil, ""); err == nil {
			t.Fatal("accepted unknown checker")
		}
	}
}

func TestPersistentRuntimeKeepsAuthenticatedCatalogProbe(t *testing.T) {
	for _, kind := range []string{"falkordb", "elasticsearch", "opensearch", "duckdb"} {
		r, c, labels, spec := persistentFixture()
		labels[owned.LabelCatalogKind], labels[owned.LabelHealthChecker] = kind, kind+"-v1"
		iid := id.New(id.PrefixInstance)
		if _, err := r.Apply(t.Context(), iid, spec, labels); err != nil {
			t.Fatal(err)
		}
		set, err := c.Kube.AppsV1().StatefulSets("selected").Get(t.Context(), WorkloadName(iid), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		probe := set.Spec.Template.Spec.Containers[0].ReadinessProbe
		command, _ := engineprobe.Command(kind, kind+"-v1")
		if probe == nil || probe.Exec == nil || !reflect.DeepEqual(probe.Exec.Command, command) || probe.TCPSocket != nil || probe.HTTPGet != nil {
			t.Fatalf("%s lost authenticated persistent readiness: %+v", kind, probe)
		}
	}
}

package deployments

import (
	"encoding/json"
	"testing"

	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

func TestFirecrackerSpecRejectsUnisolatedCapabilities(t *testing.T) {
	for _, sp := range []cpprovider.ServiceSpec{{Volumes: []cpprovider.VolumeSpec{{Name: "disk"}}}, {Resources: cpprovider.ResourceSpec{GPU: "nvidia"}}, {Resources: cpprovider.ResourceSpec{Replicas: 2}}, {Ports: []cpprovider.PortSpec{{Protocol: "udp"}}}} {
		if _, err := firecrackerSpec(sp, nil); err == nil {
			t.Fatal("unsupported capability accepted", sp)
		}
	}
}
func TestFirecrackerRegistryUsesScopedToken(t *testing.T) {
	raw, err := firecrackerRegistry([]byte(`{"serveraddress":"registry.example","registrytoken":"scoped-token"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]map[string]map[string]string
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out["auths"]["registry.example"]["registrytoken"] != "scoped-token" {
		t.Fatal(string(raw))
	}
}
func TestFirecrackerSpecRoundsUpCPUAndPinsPublishedPort(t *testing.T) {
	spec, err := firecrackerSpec(cpprovider.ServiceSpec{Resources: cpprovider.ResourceSpec{CPUMillis: 1500, MemoryMB: 512}, Ports: []cpprovider.PortSpec{{Container: 8080, Protocol: "tcp"}}}, map[string]string{owned.LabelPublic: "true", owned.LabelHostPort: "18080"})
	if err != nil || spec.VCPUs != 2 || spec.PublishedPort != 18080 || spec.Port != 8080 {
		t.Fatal(spec, err)
	}
}

package instance

import "testing"

func TestPlatformCloudDefaults(t *testing.T) {
	got := defaultPlatformCloud()
	if got.Attach != "manual" {
		t.Fatalf("attach %q, want manual", got.Attach)
	}
	if want := (PlatformLimits{VCPUs: 4, MemoryMiB: 8192, VMs: 10, ConcurrentBuilds: 1}); got.DefaultLimits != want {
		t.Fatalf("limits %+v, want %+v", got.DefaultLimits, want)
	}
	if got.Apex != "" || got.Registry != (RegistrySettings{}) {
		t.Fatalf("apex or registry set: %+v", got)
	}
}

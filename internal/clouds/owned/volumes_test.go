package owned

import (
	"testing"

	cpprovider "github.com/xraph/ctrlplane/provider"
)

func TestPersistentMountsRejectAliasesAndUnsafePaths(t *testing.T) {
	for _, volumes := range [][]cpprovider.VolumeSpec{
		{{Name: "data", Type: "bind", MountPath: "/data"}},
		{{Name: "data", Type: "persistent", MountPath: "/"}},
		{{Name: "data", Type: "persistent", MountPath: "/data/../etc"}},
		{{Name: "data", Type: "persistent", MountPath: "/data"}, {Name: "data", Type: "persistent", MountPath: "/other"}},
		{{Name: "data", Type: "persistent", MountPath: "/data"}, {Name: "logs", Type: "persistent", MountPath: "/data/log"}},
	} {
		if ValidatePersistentVolumes(volumes) == nil {
			t.Fatalf("unsafe mounts accepted: %+v", volumes)
		}
	}
	if err := ValidatePersistentVolumes([]cpprovider.VolumeSpec{{Name: "data", Type: "persistent", MountPath: "/var/lib/database", SizeMB: 1024}}); err != nil {
		t.Fatal(err)
	}
}

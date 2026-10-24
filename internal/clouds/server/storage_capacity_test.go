package server

import (
	"os/exec"
	"strings"
	"testing"

	cpprovider "github.com/xraph/ctrlplane/provider"
)

func TestCapacityDockerDataFilesystem(t *testing.T) {
	command, err := storageCapacityCommand("/mnt/docker-data")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.TrimSuffix(strings.TrimPrefix(command, "python3 - <<'PY'\n"), "\nPY")
	// The root filesystem has different facts. Sampling '/' would produce 9000.
	script = strings.Replace(script, "assert len(pids)==1, 'exact local Docker daemon required'", "pids=['4242']", 1)
	script = strings.Replace(script, "s=os.statvfs(data)", `class Facts:
 f_frsize=100
 f_blocks=7 if data=='/proc/4242/root/mnt/docker-data' else 90
 f_bavail=3 if data=='/proc/4242/root/mnt/docker-data' else 80
s=Facts()`, 1)
	raw, err := exec.CommandContext(t.Context(), "python3", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("filesystem script: %v %s", err, raw)
	}
	got, err := parseFilesystemCapacity(raw)
	if err != nil || got.TotalBytes != 700 || got.FreeBytes != 300 {
		t.Fatalf("wrong filesystem: %+v %v", got, err)
	}
	for _, bad := range []string{"", "relative", "/mnt/../root"} {
		if _, err = storageCapacityCommand(bad); err == nil {
			t.Fatalf("accepted root %q", bad)
		}
	}
	for _, bad := range []string{`{"total_bytes":-1,"free_bytes":0}`, `{"total_bytes":100,"free_bytes":101}`, `{"total_bytes":9223372036854775808,"free_bytes":0}`} {
		if _, err = parseFilesystemCapacity([]byte(bad)); err == nil {
			t.Fatal("invalid filesystem fact accepted")
		}
	}
}
func TestCapacityLocalDockerVolumeQuotasUnsupported(t *testing.T) {
	if err := validateVolumes([]cpprovider.VolumeSpec{{Name: "data", MountPath: "/data", SizeMB: 100, Type: "persistent"}}); err == nil || !strings.Contains(err.Error(), "size quotas are unsupported") {
		t.Fatalf("Docker quota response: %v", err)
	}
}

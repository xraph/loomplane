package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func capacityFixture(t *testing.T, disabled string) (string, string) {
	t.Helper()
	directory := t.TempDir()
	root := filepath.Join(directory, "cgroup")
	proc := filepath.Join(directory, "proc")
	files := map[string]string{
		"cgroup/cgroup.controllers": "cpu memory cpuset", "cgroup/cgroup.subtree_control": "cpu memory cpuset", "cgroup/cpuset.cpus.effective": "0-3",
		"cgroup/system.slice/cgroup.subtree_control": disabled, "cgroup/system.slice/cpu.max": "100000 100000", "cgroup/system.slice/memory.max": "1073741824", "cgroup/system.slice/cpuset.cpus.effective": "0-1",
		"proc/4242/comm": "dockerd", "proc/4242/cgroup": "0::/system.slice/docker.service", "proc/4243/comm": "python3", "proc/4243/cgroup": "0::/system.slice/session.scope",
	}
	for _, leaf := range []string{"docker.service", "session.scope"} {
		dir := filepath.Join(root, "system.slice", leaf)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, controller := range strings.Fields(disabled) {
			switch controller {
			case "cpu":
				files["cgroup/system.slice/"+leaf+"/cpu.max"] = "max 100000"
			case "memory":
				files["cgroup/system.slice/"+leaf+"/memory.max"] = "max"
			case "cpuset":
				files["cgroup/system.slice/"+leaf+"/cpuset.cpus.effective"] = "0-1"
			}
		}
	}
	for path, value := range files {
		name := filepath.Join(directory, path)
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root, proc
}

func runCapacityFixture(t *testing.T, root, proc string) ([]byte, error) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required")
	}
	script := strings.TrimSuffix(strings.TrimPrefix(effectiveCapacityCommand, "python3 - <<'PY'\n"), "\nPY")
	script = strings.ReplaceAll(script, "/sys/fs/cgroup", root)
	script = strings.ReplaceAll(script, "/proc/", proc+"/")
	script = strings.ReplaceAll(script, "str(os.getpid())", "'4243'")
	script = strings.ReplaceAll(script, "f.split('/')[2]", "pathlib.Path(f).parent.name")
	return exec.CommandContext(t.Context(), python, "-c", script).CombinedOutput()
}

func TestEffectiveCapacityInheritedControllers(t *testing.T) {
	for _, enabled := range []string{"cpu memory", "", "cpu memory cpuset"} {
		t.Run(enabled, func(t *testing.T) {
			root, proc := capacityFixture(t, enabled)
			raw, err := runCapacityFixture(t, root, proc)
			if err != nil {
				t.Fatalf("valid inherited layout rejected: %v: %s", err, raw)
			}
			var capacity struct {
				CPU    int64 `json:"cpu"`
				Memory int64 `json:"memory"`
			}
			if err = json.Unmarshal(raw, &capacity); err != nil {
				t.Fatal(err)
			}
			if capacity.CPU != 1000 || capacity.Memory != 1<<30 {
				t.Fatalf("ancestor limit lost: %+v", capacity)
			}
		})
	}
}

func TestEffectiveCapacityUnknownLayoutsFailClosed(t *testing.T) {
	cases := map[string]struct {
		path, value string
		remove      bool
	}{
		"enabled cpuset missing":  {"cgroup/system.slice/docker.service/cpuset.cpus.effective", "", true},
		"enabled cpu missing":     {"cgroup/system.slice/docker.service/cpu.max", "", true},
		"enabled memory missing":  {"cgroup/system.slice/docker.service/memory.max", "", true},
		"root cpuset missing":     {"cgroup/cpuset.cpus.effective", "", true},
		"root controller unknown": {"cgroup/cgroup.controllers", "cpu memory", false},
		"empty cpuset":            {"cgroup/system.slice/docker.service/cpuset.cpus.effective", "", false},
		"overlapping cpuset":      {"cgroup/system.slice/docker.service/cpuset.cpus.effective", "0-2,1-3", false},
		"reversed cpuset":         {"cgroup/system.slice/docker.service/cpuset.cpus.effective", "3-1", false},
		"zero period":             {"cgroup/system.slice/docker.service/cpu.max", "max 0", false},
		"malformed memory":        {"cgroup/system.slice/docker.service/memory.max", "unknown", false},
		"hidden hierarchy":        {"proc/4242/cgroup", "0::/hidden/docker", false},
		"traversal":               {"proc/4242/cgroup", "0::/../docker", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root, proc := capacityFixture(t, "cpu memory cpuset")
			path := filepath.Join(filepath.Dir(root), tc.path)
			var err error
			if tc.remove {
				err = os.Remove(path)
			} else {
				err = os.WriteFile(path, []byte(tc.value), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if raw, err := runCapacityFixture(t, root, proc); err == nil {
				t.Fatalf("unknown layout admitted: %s", raw)
			}
		})
	}
	t.Run("missing parent controller evidence", func(t *testing.T) {
		root, proc := capacityFixture(t, "")
		if err := os.Remove(filepath.Join(root, "system.slice/cgroup.subtree_control")); err != nil {
			t.Fatal(err)
		}
		if raw, err := runCapacityFixture(t, root, proc); err == nil {
			t.Fatalf("unproven inheritance admitted: %s", raw)
		}
	})
}

func TestEffectiveCapacityKeepsProbeAndDaemonMinima(t *testing.T) {
	root, proc := capacityFixture(t, "cpu memory cpuset")
	values := map[string]string{"docker.service/cpu.max": "75000 100000", "session.scope/memory.max": "536870912"}
	for path, value := range values {
		if err := os.WriteFile(filepath.Join(root, "system.slice", path), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := runCapacityFixture(t, root, proc)
	if err != nil {
		t.Fatalf("read minima: %v: %s", err, raw)
	}
	var capacity struct {
		CPU    int64 `json:"cpu"`
		Memory int64 `json:"memory"`
	}
	if err = json.Unmarshal(raw, &capacity); err != nil {
		t.Fatal(err)
	}
	if capacity.CPU != 750 || capacity.Memory != 1<<29 {
		t.Fatalf("leaf minima lost: %+v", capacity)
	}
}

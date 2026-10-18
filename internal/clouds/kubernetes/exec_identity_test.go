package kubernetes

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExecIdentityGateRejectsReusedPodName(t *testing.T) {
	for _, actual := range []string{"expected-pod", "replacement-pod"} {
		t.Run(actual, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "command-ran")
			args := podCommand("expected-pod", []string{"touch", path})
			command := exec.CommandContext(t.Context(), args[0], args[1:]...)
			command.Env = append(os.Environ(), "LOOMPLANE_POD_UID="+actual)
			err := command.Run()
			_, statErr := os.Stat(path)
			if actual == "expected-pod" {
				if err != nil || statErr != nil {
					t.Fatalf("owned pod command did not run: %v %v", err, statErr)
				}
			} else if err == nil || !os.IsNotExist(statErr) {
				t.Fatalf("command ran in a replacement pod: %v %v", err, statErr)
			}
		})
	}
}

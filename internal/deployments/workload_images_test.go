package deployments

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestPodGroupArchitectureRequiresEveryImage(t *testing.T) {
	main := "registry.example.test/main@sha256:" + strings.Repeat("a", 64)
	group := &provider.PodGroup{Sidecars: []provider.Sidecar{{Name: "helper", Image: "registry.example.test/helper@sha256:" + strings.Repeat("b", 64), Architecture: "arm64", CPUMillis: 250, MemoryMiB: 128, Readiness: provider.ContainerReadiness{Port: 9001}}}}
	seen := []string{}
	mismatch := errors.New("platform mismatch")
	verify := func(_ context.Context, image, os, architecture string) error {
		seen = append(seen, image)
		if os != "linux" || architecture != "arm64" {
			t.Fatal("wrong requested platform")
		}
		if image == group.Sidecars[0].Image {
			return mismatch
		}
		return nil
	}
	if err := verifyPodGroupImages(t.Context(), main, "arm64", group, verify); !errors.Is(err, mismatch) {
		t.Fatal("unverified sidecar accepted", err)
	}
	if len(seen) != 2 {
		t.Fatal("not every image was verified", seen)
	}
	seen = nil
	if err := verifyPodGroupImages(t.Context(), main, "amd64", group, verify); err == nil || len(seen) != 0 {
		t.Fatal("declared mismatch reached registry")
	}
}

package azure

import (
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/azure/azuretest"
)

// A finished run names each image it pushed, with its digest.
func TestWaitBuild_readsTheImagesTheRunPushed(t *testing.T) {
	f := azuretest.New(t)
	s := fakeSession(t, f)
	if err := s.EnsureRegistered(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureGroup(t.Context(), "eastus"); err != nil {
		t.Fatal(err)
	}
	reg, err := s.EnsureRegistry(t.Context(), "eastus")
	if err != nil {
		t.Fatal(err)
	}
	where, err := s.UploadSource(t.Context(), strings.NewReader("tarball"), int64(len("tarball")))
	if err != nil {
		t.Fatal(err)
	}
	runID, err := s.StartBuild(t.Context(), BuildRequest{Source: where, Image: "api-x:abc123def456", Dockerfile: "src/Dockerfile", Context: "src"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.WaitBuild(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	images := run.Properties.OutputImages
	if len(images) != 1 {
		t.Fatalf("output images %+v", images)
	}
	got := images[0]
	if got.Registry != reg.Properties.LoginServer || got.Repository != "api-x" || got.Tag != "abc123def456" ||
		got.Digest != azuretest.Digest("api-x:abc123def456") {
		t.Fatalf("output image %+v", got)
	}
}

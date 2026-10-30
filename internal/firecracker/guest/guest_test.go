package guest

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fc "github.com/xraph/loomplane/internal/firecracker"
)

func TestExtractRejectsTraversalAndLinks(t *testing.T) {
	for _, name := range []string{"../secret", "/etc/passwd", "link"} {
		t.Run(name, func(t *testing.T) {
			var b bytes.Buffer
			tw := tar.NewWriter(&b)
			h := &tar.Header{Name: name, Mode: 0o644, Size: 1}
			if name == "link" {
				h.Typeflag = tar.TypeSymlink
				h.Linkname = "/run/secret"
				h.Size = 0
			}
			if err := tw.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
			if h.Size > 0 {
				_, _ = tw.Write([]byte("x"))
			}
			_ = tw.Close()
			if err := extract(t.TempDir(), &b); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}
func TestExtractPreservesExecutableSource(t *testing.T) {
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	_ = tw.WriteHeader(&tar.Header{Name: "app/run.sh", Mode: 0o755, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	root := t.TempDir()
	if err := extract(root, &b); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "app/run.sh"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatal("lost source", err)
	}
}
func TestAppArgumentsKeepSecretsOutOfArgv(t *testing.T) {
	s := fc.Spec{Image: "registry/app@sha256:" + strings.Repeat("a", 64), Command: []string{"/app/start", "--worker"}, Args: []string{"fast"}}
	args := appArgs(s, "/run/env")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--env-file /run/env") || !strings.Contains(joined, "--entrypoint /app/start") || !strings.HasSuffix(joined, "--worker fast") {
		t.Fatal(args)
	}
}

func TestBuildKitMetadataAcceptsDescriptorObject(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	got, err := imageDigest([]byte(`{"containerimage.digest":"` + digest + `","containerimage.descriptor":{"mediaType":"application/vnd.oci.image.manifest.v1+json","size":1234,"digest":"` + digest + `"},"buildx.build.provenance":{}}`))
	if err != nil || got != digest {
		t.Fatal(got, err)
	}
	for _, raw := range []string{`{}`, `{"containerimage.digest":"latest"}`, `{"containerimage.digest":{}}`} {
		if _, err = imageDigest([]byte(raw)); err == nil {
			t.Fatal("invalid digest accepted", raw)
		}
	}
}

package deployments

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/planning"
)

func TestShortName_fitsAndStaysUnique(t *testing.T) {
	a := shortName("a-very-long-service-name-in-production-abcdefgh")
	b := shortName("a-very-long-service-name-in-production-abcdefgi")
	if len(a) > 32 || len(b) > 32 || a == b || !strings.HasPrefix(a, "lp-a-very-long") {
		t.Fatalf("%q and %q", a, b)
	}
	if got := shortName("api-production-x1"); len(got) > 32 || strings.Contains(got, "--") {
		t.Fatalf("%q", got)
	}
}

func TestBuildspec(t *testing.T) {
	s := aws.New("").Session(&aws.Key{AccessKeyID: "AKIAEXAMPLE0000", SecretAccessKey: "x"}, "eu-west-1", "123456789012")
	docker := buildspec(s, &planning.ServiceConfig{Image: "services/api/Dockerfile"}, "repo:sha")
	for _, want := range []string{
		`docker login --username AWS --password-stdin 123456789012.dkr.ecr.eu-west-1.amazonaws.com`,
		`cd \"$CODEBUILD_SRC_DIR/services/api\" \u0026\u0026 docker build -t repo:sha -f Dockerfile .`,
		`docker push repo:sha`,
	} {
		if !strings.Contains(docker, want) {
			t.Fatalf("docker buildspec lacks %s:\n%s", want, docker)
		}
	}
	root := buildspec(s, &planning.ServiceConfig{Image: "services/api/Dockerfile", Context: "."}, "repo:sha")
	if want := `cd \"$CODEBUILD_SRC_DIR/.\" \u0026\u0026 docker build -t repo:sha -f services/api/Dockerfile .`; !strings.Contains(root, want) {
		t.Fatalf("buildspec from the root lacks %s:\n%s", want, root)
	}
	pack := buildspec(s, &planning.ServiceConfig{Ecosystem: "go", Source: "services/api"}, "repo:sha")
	if !strings.Contains(pack, "pack build repo:sha --builder "+googleBuilder+" --publish --env GOOGLE_BUILDABLE=./services/api") ||
		!strings.Contains(pack, "install:") {
		t.Fatalf("pack buildspec:\n%s", pack)
	}
}

func TestTarToZip_dropsTheTopDirectory(t *testing.T) {
	var tgz bytes.Buffer
	zw := gzip.NewWriter(&tgz)
	tw := tar.NewWriter(zw)
	for name, body := range map[string]string{"acme-mono-1/go.mod": "module x", "acme-mono-1/services/api/main.go": "package main"} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.WriteHeader(&tar.Header{Name: "acme-mono-1/", Typeflag: tar.TypeDir, Mode: 0o755})
	_ = tw.Close()
	_ = zw.Close()
	job := &Job{Source: func(context.Context) (io.ReadCloser, int64, error) {
		return io.NopCloser(bytes.NewReader(tgz.Bytes())), int64(tgz.Len()), nil
	}}
	var out bytes.Buffer
	if err := tarToZip(t.Context(), job, &out); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if len(names) != 2 || !names["go.mod"] || !names["services/api/main.go"] {
		t.Fatalf("zip holds %v", names)
	}
}

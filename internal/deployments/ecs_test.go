package deployments

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/connections"
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
	py := buildspec(s, &planning.ServiceConfig{Ecosystem: "python", Source: "api", Start: "celery -A it's worker"}, "repo:sha")
	if want := `--env 'GOOGLE_ENTRYPOINT=celery -A it'\\''s worker'`; !strings.Contains(py, want) {
		t.Fatalf("python buildspec lacks %s:\n%s", want, py)
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

// ECR's answers to reading a pushed image's digest, besides the digest
// itself.
const (
	ecrNotFound = ""       // ImageNotFoundException
	ecrDenied   = "denied" // AccessDeniedException
	ecrNoImage  = "none"   // an answer that lists no image
)

// ecrFake is the AWS behind an ECS build that CodeBuild has started:
// CodeBuild finishes it, and ECR answers for the image it pushed with
// digest, or as one of the modes above says.
func ecrFake(t *testing.T, digest string) *httptest.Server {
	t.Helper()
	const uri = "123456789012.dkr.ecr.eu-west-1.amazonaws.com/loomplane/api-23456789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		target := r.Header.Get("X-Amz-Target")
		switch {
		case strings.HasSuffix(target, ".DescribeRepositories"):
			_, _ = w.Write([]byte(`{"repositories":[{"repositoryName":"loomplane/api-23456789","repositoryUri":"` + uri + `"}]}`))
		case strings.HasSuffix(target, ".BatchGetBuilds"):
			_, _ = w.Write([]byte(`{"builds":[{"id":"loomplane:1","buildStatus":"SUCCEEDED"}]}`))
		case strings.HasSuffix(target, ".DescribeImages"):
			ids, _ := in["imageIds"].([]any)
			switch digest {
			case ecrDenied:
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"__type":"AccessDeniedException","message":"not authorized to perform: ecr:DescribeImages"}`))
				return
			case ecrNoImage:
				_, _ = w.Write([]byte(`{"imageDetails":[]}`))
				return
			}
			if digest == ecrNotFound || in["repositoryName"] != "loomplane/api-23456789" || len(ids) != 1 || ids[0].(map[string]any)["imageTag"] != doApproved[:12] {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"__type":"ImageNotFoundException","message":"The image requested does not exist"}`))
				return
			}
			_, _ = w.Write([]byte(`{"imageDetails":[{"repositoryName":"loomplane/api-23456789","imageDigest":"` + digest + `"}]}`))
		default:
			t.Errorf("asked %s", target)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func ecsBuild(t *testing.T, digest string) (string, error) {
	t.Helper()
	image, _, err := ecsBuildState(t, digest)
	return image, err
}

func ecsBuildState(t *testing.T, digest string) (string, *ecsState, error) {
	t.Helper()
	srv := ecrFake(t, digest)
	exec := &ECS{AWS: aws.New(srv.URL)}
	job := azureJob(t, apiConfig())
	job.Connection = &connections.Connection{ID: "conn_a", Cloud: connections.AWS, Account: "123456789012", Details: map[string]string{"region": "eu-west-1"}}
	s, err := exec.session(job, []byte(`{"access_key_id":"AKIATESTTESTTESTTEST","secret_access_key":"secret-test-secret-test"}`))
	if err != nil {
		t.Fatal(err)
	}
	// CodeBuild already started the build.
	state := &ecsState{BuildID: "loomplane:1", LogAttempt: "attempt"}
	image, err := exec.build(t.Context(), s, job, state, func() {})
	return image, state, err
}

func TestECS_deploysTheBuiltImageByDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("e5", 32)
	image, err := ecsBuild(t, digest)
	if want := "123456789012.dkr.ecr.eu-west-1.amazonaws.com/loomplane/api-23456789@" + digest; err != nil || image != want {
		t.Fatalf("built %q, %v; want %s", image, err, want)
	}
}

// A connection that may not read ECR's digests deploys the image by tag,
// as before images were pinned, and says why.
func TestECS_deploysByTagWhenItMayNotReadTheDigest(t *testing.T) {
	image, state, err := ecsBuildState(t, ecrDenied)
	if want := "123456789012.dkr.ecr.eu-west-1.amazonaws.com/loomplane/api-23456789:" + doApproved[:12]; err != nil || image != want {
		t.Fatalf("built %q, %v; want %s", image, err, want)
	}
	if state.Unpinned != "ecr:DescribeImages" {
		t.Fatalf("checkpoint %+v", state)
	}
	if got := unpinnedWarning("api", state.Unpinned).Error(); !strings.Contains(got, "ecr:DescribeImages") ||
		!strings.Contains(got, "promoting or rolling back api builds it again") {
		t.Fatalf("warning %q", got)
	}
}

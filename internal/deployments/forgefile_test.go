package deployments

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
)

func buildArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var out bytes.Buffer
	z := gzip.NewWriter(&out)
	w := tar.NewWriter(z)
	for name, data := range files {
		if err := w.WriteHeader(&tar.Header{Name: "repo-sha/" + name, Mode: 0o644, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func contextFiles(t *testing.T, data []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	r := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		out[h.Name] = string(body)
	}
}

func TestForgefile_nestedModuleUsesDeclaredEntrypointAndWorkspace(t *testing.T) {
	input := buildArchive(t, map[string]string{"go.work": "go 1.26.0\nuse (\n ./services/api\n ./libs/shared\n)\n", "services/api/go.mod": "module example/api\ngo 1.26.0\nreplace example/shared => ../../libs/shared\n", "libs/shared/go.mod": "module example/shared\ngo 1.26.0\n", "services/api/cmd/serve/main.go": "package main\nfunc main(){}\n", "examples/demo/main.go": "package main\nfunc main(){}\n"})
	prepared, err := prepareImageContext(bytes.NewReader(input), &planning.ServiceConfig{Ecosystem: "go", Framework: "forge", Source: "services/api/cmd/serve"})
	if err != nil {
		t.Fatal(err)
	}
	files := contextFiles(t, prepared.Tar)
	dockerfile := files[prepared.Dockerfile]
	for _, need := range []string{"FROM golang:1.26", "WORKDIR /src/services/api", `"go","build","-trimpath"`, "./cmd/serve", "ENTRYPOINT"} {
		if !strings.Contains(dockerfile, need) {
			t.Fatalf("missing %q in %s", need, dockerfile)
		}
	}
	if files["libs/shared/go.mod"] == "" || files["go.work"] == "" {
		t.Fatal("workspace module context was lost")
	}
}

func TestImageBuildContext_relocatesDockerfileAndHonorsIgnore(t *testing.T) {
	input := buildArchive(t, map[string]string{"docker/api.Dockerfile": "FROM scratch\nCOPY marker /marker\n", "services/api/marker": "approved source", "services/api/.dockerignore": "private.txt\n", "services/api/private.txt": "ignored", "services/other/marker": "other service"})
	prepared, err := prepareImageContext(bytes.NewReader(input), &planning.ServiceConfig{Image: "docker/api.Dockerfile", Context: "services/api"})
	if err != nil {
		t.Fatal(err)
	}
	files := contextFiles(t, prepared.Tar)
	if files["marker"] != "approved source" || files[prepared.Dockerfile] != "FROM scratch\nCOPY marker /marker\n" {
		t.Fatalf("context: %+v", files)
	}
	if _, ok := files["private.txt"]; ok {
		t.Fatal("dockerignore excluded file was sent")
	}
	if _, ok := files["services/other/marker"]; ok {
		t.Fatal("another service escaped the chosen context")
	}
}

func TestForgefile_rejectsMissingEntrypointAndEscapingModules(t *testing.T) {
	for _, cfg := range []*planning.ServiceConfig{{Ecosystem: "go", Source: "cmd/missing"}, {Ecosystem: "go", Source: "services/api", Context: "services/api"}} {
		input := buildArchive(t, map[string]string{"services/api/main.go": "package main\nfunc main(){}\n", "services/api/go.mod": "module example/api\ngo 1.26.0\nreplace example/shared => ../../libs/shared\n", "libs/shared/go.mod": "module example/shared\ngo 1.26.0\n"})
		if _, err := prepareImageContext(bytes.NewReader(input), cfg); err == nil {
			t.Fatalf("unsupported build accepted: %+v", cfg)
		}
	}
	input := buildArchive(t, map[string]string{"../escape": "escaped", "Dockerfile": "FROM scratch\n"})
	if _, err := prepareImageContext(bytes.NewReader(input), &planning.ServiceConfig{Image: "Dockerfile"}); err == nil {
		t.Fatal("unsafe archive path accepted")
	}
}

func TestForgefile_doesNotChooseExamplesUnlessTheEntrypointNamesOne(t *testing.T) {
	archive := buildArchive(t, map[string]string{"go.mod": "module example/root\ngo 1.26.0\n", "cmd/api/main.go": "package main\nfunc main(){}\n", "examples/demo/main.go": "package main\nfunc main(){}\n"})
	for _, source := range []string{"cmd/api", "examples/demo"} {
		prepared, err := prepareImageContext(bytes.NewReader(archive), &planning.ServiceConfig{Ecosystem: "go", Framework: "forge", Source: source})
		if err != nil {
			t.Fatal(err)
		}
		file := contextFiles(t, prepared.Tar)[prepared.Dockerfile]
		if !strings.Contains(file, `"./`+source+`"`) {
			t.Fatalf("declared app not selected: %s", file)
		}
	}
	if _, err := prepareImageContext(bytes.NewReader(archive), &planning.ServiceConfig{Ecosystem: "go", Framework: "forge"}); err == nil {
		t.Fatal("an example was guessed for a missing entrypoint")
	}
}

func TestForgefile_rejectsIgnoreRulesThatRemoveRequiredModules(t *testing.T) {
	archive := buildArchive(t, map[string]string{"go.mod": "module example/root\ngo 1.26.0\n", "cmd/api/main.go": "package main\nfunc main(){}\n", ".dockerignore": "go.mod\n"})
	if _, err := prepareImageContext(bytes.NewReader(archive), &planning.ServiceConfig{Ecosystem: "go", Source: "cmd/api"}); err == nil {
		t.Fatal("an excluded Go module reached the builder")
	}
}

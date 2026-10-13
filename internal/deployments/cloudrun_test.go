package deployments

import (
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
)

func TestBuildFor(t *testing.T) {
	const img = "us-central1-docker.pkg.dev/p/loomplane/api-abc:sha"
	cases := []struct {
		name string
		cfg  planning.ServiceConfig
		step string
		dir  string
		args []string
	}{
		{"a dockerfile builds from its own directory", planning.ServiceConfig{Image: "services/api/Dockerfile"},
			dockerBuilder, "acme-mono-1/services/api", []string{"build", "-t", img, "-f", "Dockerfile", "."}},
		{"one at the root builds from the root", planning.ServiceConfig{Image: "Dockerfile.prod"},
			dockerBuilder, "acme-mono-1", []string{"build", "-t", img, "-f", "Dockerfile.prod", "."}},
		{"a build context from the root names the Dockerfile from there", planning.ServiceConfig{Image: "services/api/Dockerfile", Context: "."},
			dockerBuilder, "acme-mono-1", []string{"build", "-t", img, "-f", "services/api/Dockerfile", "."}},
		{"go without one uses the buildpack on its main package", planning.ServiceConfig{Ecosystem: "go", Source: "services/api"},
			packBuilder, "", []string{"build", img, "--builder", googleBuilder, "--publish", "--env", "GOOGLE_BUILDABLE=./services/api", "--path", "acme-mono-1"}},
		{"a jvm module builds from the root of its build", planning.ServiceConfig{Ecosystem: "jvm", Source: "services/api"},
			packBuilder, "", []string{"build", img, "--builder", googleBuilder, "--publish", "--env", "GOOGLE_BUILDABLE=services/api", "--path", "acme-mono-1"}},
		{"python without one builds its project", planning.ServiceConfig{Ecosystem: "python", Source: "services/api"},
			packBuilder, "", []string{"build", img, "--builder", googleBuilder, "--publish", "--path", "acme-mono-1/services/api"}},
		{"a python process says how it starts", planning.ServiceConfig{Ecosystem: "python", Source: "services/api", Start: "celery -A shop worker"},
			packBuilder, "", []string{"build", img, "--builder", googleBuilder, "--publish", "--env", "GOOGLE_ENTRYPOINT=celery -A shop worker", "--path", "acme-mono-1/services/api"}},
		{"node without one builds its package", planning.ServiceConfig{Ecosystem: "node", Source: "apps/gateway"},
			packBuilder, "", []string{"build", img, "--builder", googleBuilder, "--publish", "--path", "acme-mono-1/apps/gateway"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := buildFor(&tc.cfg, "acme-mono-1", img)
			if len(b.Steps) != 1 || b.Steps[0].Name != tc.step || b.Steps[0].Dir != tc.dir || !slices.Equal(b.Steps[0].Args, tc.args) {
				t.Fatalf("steps %+v", b.Steps)
			}
			if (tc.step == dockerBuilder) != (len(b.Images) == 1) {
				t.Fatalf("images %v", b.Images)
			}
		})
	}
}

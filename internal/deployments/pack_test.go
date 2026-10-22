package deployments

import (
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
)

func TestPackPlan(t *testing.T) {
	for _, tt := range []struct {
		cfg  planning.ServiceConfig
		path string
		env  []string
	}{
		{planning.ServiceConfig{Ecosystem: "go", Source: "services/api"}, ".", []string{"GOOGLE_BUILDABLE=./services/api"}},
		{planning.ServiceConfig{Ecosystem: "jvm", Source: "billing"}, ".", []string{"GOOGLE_BUILDABLE=billing"}},
		{planning.ServiceConfig{Ecosystem: "jvm", Source: "."}, ".", nil},
		{planning.ServiceConfig{Ecosystem: "node", Source: "apps/web"}, "apps/web", nil},
		{planning.ServiceConfig{Ecosystem: "python", Source: "worker", Start: "celery -A it worker"}, "worker", []string{"GOOGLE_ENTRYPOINT=celery -A it worker"}},
	} {
		path, env := packPlan(&tt.cfg)
		if path != tt.path || !slices.Equal(env, tt.env) {
			t.Errorf("%+v: %q %q", tt.cfg, path, env)
		}
	}
}

package detect

import (
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/discovery"
)

func TestGo_entrypointsReachTheirOwnConstructors(t *testing.T) {
	r := run(t, Go{}, map[string]string{
		"go.mod":             "module example.com/app\n\ngo 1.26\n",
		"cmd/api/main.go":    "package main\nimport \"example.com/app/internal/platform\"\nfunc main() { platform.API() }\n",
		"cmd/worker/main.go": "package main\nimport (\n\"example.com/app/internal/platform\"\n\"github.com/xraph/forge/cli\"\n)\nfunc main() { cli.NewAppRunner(platform.Worker).Run() }\n",
		"internal/platform/platform.go": `package platform
import (
 "github.com/gin-gonic/gin"
 "github.com/jackc/pgx/v5"
 redis "github.com/redis/go-redis/v9"
 "github.com/aws/aws-sdk-go-v2/service/s3"
)
func API() { gin.New(); pgx.Connect(nil, "") }
func Worker() { queue() }
func queue() { redis.NewClient(nil) }
func unusedProvider() { s3.NewFromConfig(nil) }
`,
	})
	u := onlyUnit(t, r, "go:.")
	for _, ep := range u.Entrypoints {
		want := "postgres"
		if ep.Name == "worker" {
			want = "redis"
			for _, fw := range ep.Frameworks {
				if fw.Name == "gin" {
					t.Errorf("worker inherited API framework: %+v", fw)
				}
			}
		}
		if len(ep.Resources) != 1 || ep.Resources[0].Kind != want {
			t.Errorf("%s resources = %+v, want only %s", ep.Name, ep.Resources, want)
		}
	}
}

func TestGo_forgeDriverOptionsAreEvidenceWithoutSecrets(t *testing.T) {
	r := run(t, Go{}, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.26\n",
		"main.go": `package main
import groveext "github.com/xraph/grove/extension"
func main() { groveext.New(groveext.WithDSN("postgres", "postgres://secret:password@db/app")) }
`,
	})
	u := onlyUnit(t, r, "go:.")
	if len(u.Entrypoints) != 1 || len(u.Entrypoints[0].Resources) != 1 {
		t.Fatalf("Forge database option not detected: %+v", u.Entrypoints)
	}
	res := u.Entrypoints[0].Resources[0]
	if res.Kind != "postgres" || res.Confidence != discovery.Certain || res.Evidence.Line != 3 {
		t.Fatalf("Forge database evidence: %+v", res)
	}
	if strings.Contains(res.Evidence.Detail, "password") || strings.Contains(res.Evidence.Detail, "secret") {
		t.Fatalf("resource evidence contains credentials: %+v", res)
	}
}

func TestGo_importedPackageInitializationStillCounts(t *testing.T) {
	r := run(t, Go{}, map[string]string{
		"go.mod":                   "module example.com/app\n\ngo 1.26\n",
		"main.go":                  "package main\nimport _ \"example.com/app/internal/startup\"\nfunc main() {}\n",
		"internal/startup/init.go": "package startup\nimport redis \"github.com/redis/go-redis/v9\"\nfunc init() { redis.NewClient(nil) }\n",
	})
	u := onlyUnit(t, r, "go:.")
	if len(u.Entrypoints[0].Resources) != 1 || u.Entrypoints[0].Resources[0].Kind != "redis" {
		t.Fatalf("package initialization lost: %+v", u.Entrypoints)
	}
}

func TestGo_constructorReceiversReachTheirMethods(t *testing.T) {
	for _, invoke := range []string{"service := app.New(); service.Start()", "app.New().Start()"} {
		t.Run(invoke, func(t *testing.T) {
			r := run(t, Go{}, map[string]string{
				"go.mod":  "module example.com/product\ngo 1.26\n",
				"main.go": "package main\nimport \"example.com/product/internal/app\"\nfunc main() { " + invoke + " }\n",
				"internal/app/app.go": `package app
import groveext "github.com/xraph/grove/extension"
type App struct{}
func New() *App { return &App{} }
func (*App) Start() { groveext.New(groveext.WithDSN("postgres", "")) }
`,
			})
			u := onlyUnit(t, r, "go:.")
			if len(u.Entrypoints[0].Resources) != 1 || u.Entrypoints[0].Resources[0].Kind != "postgres" {
				t.Fatalf("receiver resources: %+v", u.Entrypoints[0].Resources)
			}
		})
	}
}

func TestGo_forgeDriverOptionsStrengthenImportEvidence(t *testing.T) {
	r := run(t, Go{}, map[string]string{
		"go.mod": "module example.com/product\ngo 1.26\n",
		"main.go": `package main
import (
 groveext "github.com/xraph/grove/extension"
 "github.com/xraph/grove/drivers/pgdriver"
)
func main() { pgdriver.New(); groveext.New(groveext.WithDSN("postgres", "")) }
`,
	})
	u := onlyUnit(t, r, "go:.")
	if len(u.Entrypoints[0].Resources) != 1 || u.Entrypoints[0].Resources[0].Confidence != discovery.Certain {
		t.Fatalf("configured driver remained uncertain: %+v", u.Entrypoints[0].Resources)
	}
}

func TestGo_sameNamedMethodsDoNotMixReceiverBackends(t *testing.T) {
	r := run(t, Go{}, map[string]string{
		"go.mod":  "module example.com/product\ngo 1.26\n",
		"main.go": "package main\nimport \"example.com/product/internal/app\"\nfunc main() { worker := app.NewWorker(); worker.Start() }\n",
		"internal/app/app.go": `package app
import (
 "github.com/jackc/pgx/v5"
 redis "github.com/redis/go-redis/v9"
)
type API struct{}
type Worker struct{}
func NewWorker() *Worker { return &Worker{} }
func (*API) Start() { pgx.Connect(nil, "") }
func (*Worker) Start() { redis.NewClient(nil) }
`,
	})
	u := onlyUnit(t, r, "go:.")
	if len(u.Entrypoints[0].Resources) != 1 || u.Entrypoints[0].Resources[0].Kind != "redis" {
		t.Fatalf("receiver backend leak: %+v", u.Entrypoints[0].Resources)
	}
}

func TestGo_localVariablesDoNotReachShadowedFunctions(t *testing.T) {
	r := run(t, Go{}, map[string]string{
		"go.mod": "module example.com/product\ngo 1.26\n",
		"main.go": `package main
import (
 "fmt"
 redis "github.com/redis/go-redis/v9"
)
func main() { cache := 42; fmt.Println(cache) }
func cache() { redis.NewClient(nil) }
`,
	})
	u := onlyUnit(t, r, "go:.")
	if len(u.Entrypoints[0].Resources) != 0 {
		t.Fatalf("shadowed function became reachable: %+v", u.Entrypoints[0].Resources)
	}
}

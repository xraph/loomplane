package detect

import (
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/discovery"
)

func envNamed(t *testing.T, r *discovery.Report, name string) discovery.EnvUse {
	t.Helper()
	for _, v := range r.Env {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("no variable %s in %+v", name, r.Env)
	return discovery.EnvUse{}
}

func envNames(r *discovery.Report) string {
	names := make([]string, len(r.Env))
	for i, v := range r.Env {
		names[i] = v.Name
	}
	return strings.Join(names, " ")
}

func TestEnv_readsEachLanguage(t *testing.T) {
	r := run(t, Env{}, map[string]string{
		"cmd/api/main.go":                        "package main\nimport \"os\"\nvar db = os.Getenv(\"DATABASE_URL\")\nvar _, ok = os.LookupEnv(\"DEBUG\")\n",
		"internal/config/config.go":              "package config\ntype C struct {\n\tPort int `env:\"API_PORT\" envDefault:\"8080\"`\n\tKey string `envconfig:\"STRIPE_KEY\"`\n}\n",
		"web/src/app.ts":                         "const u = process.env.NEXT_PUBLIC_API_URL\nconst s = process.env['SESSION_SECRET']\nconst m = import.meta.env.MODE + import.meta.env.VITE_SITE\n",
		"worker/tasks.py":                        "import os\nREDIS = os.getenv('REDIS_URL', 'redis://localhost:6379')\nKEY = os.environ[\"OPENAI_API_KEY\"]\n",
		"crates/x/src/main.rs":                   "let t = std::env::var(\"RUST_TOKEN\");\n",
		"svc/src/main/java/A.java":               "String a = System.getenv(\"JAVA_HOME_URL\");\n",
		"svc/src/main/resources/application.yml": "spring:\n  datasource:\n    url: ${JDBC_URL:jdbc:postgresql://localhost/db}\n    port: ${server.port}\n",
		"app/models/a.rb":                        "ENV.fetch('RAILS_MASTER_KEY', 'x')\n",
		"site/index.php":                         "$k = env('APP_KEY');\n",
		"cmd/api/main_test.go":                   "package main\nvar _ = os.Getenv(\"TEST_ONLY\")\n",
		"web/public/vendor.min.js":               "process.env.MINIFIED",
		"scripts/run.sh":                         "echo $PATH $SHELL_ONLY\n",
	})
	want := "API_PORT APP_KEY DATABASE_URL DEBUG JAVA_HOME_URL JDBC_URL NEXT_PUBLIC_API_URL OPENAI_API_KEY RAILS_MASTER_KEY REDIS_URL RUST_TOKEN SESSION_SECRET STRIPE_KEY VITE_SITE"
	if got := envNames(r); got != want {
		t.Fatalf("names\n got %s\nwant %s", got, want)
	}
	if v := envNamed(t, r, "REDIS_URL"); v.Example != "redis://localhost:6379" || v.Confidence != discovery.Certain {
		t.Fatalf("REDIS_URL %+v", v)
	}
	if v := envNamed(t, r, "API_PORT"); v.Example != "8080" {
		t.Fatalf("API_PORT %+v", v)
	}
	if v := envNamed(t, r, "JDBC_URL"); v.Example != "jdbc:postgresql://localhost/db" {
		t.Fatalf("JDBC_URL %+v", v)
	}
	if v := envNamed(t, r, "DATABASE_URL"); len(v.Evidence) != 1 || v.Evidence[0].Line != 3 || v.Evidence[0].Path != "cmd/api/main.go" {
		t.Fatalf("DATABASE_URL evidence %+v", v.Evidence)
	}
}

func TestEnv_exampleFilesGiveValuesRealOnesOnlyKeys(t *testing.T) {
	r := run(t, Env{}, map[string]string{
		".env.example": "# the database\nexport DATABASE_URL=\"postgres://localhost/app\"\nLOG_LEVEL=info # or debug\nEMPTY=\n",
		".env":         "DATABASE_URL=postgres://admin:hunter2@prod/app\nSTRIPE_SECRET=sk_live_abc\n",
		"main.go":      "package main\nvar _ = os.Getenv(\"LOG_LEVEL\")\n",
	})
	if got := envNames(r); got != "DATABASE_URL EMPTY LOG_LEVEL STRIPE_SECRET" {
		t.Fatalf("names %s", got)
	}
	if v := envNamed(t, r, "DATABASE_URL"); v.Example != "postgres://localhost/app" {
		t.Fatalf("DATABASE_URL example %q", v.Example)
	}
	if v := envNamed(t, r, "LOG_LEVEL"); v.Example != "info" || len(v.Evidence) != 2 {
		t.Fatalf("LOG_LEVEL %+v", v)
	}
	secret := envNamed(t, r, "STRIPE_SECRET")
	if secret.Example != "" || secret.Confidence != discovery.Likely || secret.Evidence[0].Detail != "STRIPE_SECRET" {
		t.Fatalf("a real .env leaked into %+v", secret)
	}
	for _, v := range r.Env {
		for _, ev := range v.Evidence {
			if strings.Contains(ev.Detail, "hunter2") || strings.Contains(ev.Detail, "sk_live") {
				t.Fatalf("evidence quotes a real .env value: %+v", ev)
			}
		}
	}
	if len(r.Notes) != 1 || r.Notes[0].Path != ".env" {
		t.Fatalf("notes %+v", r.Notes)
	}
}

func TestEnvFileKind(t *testing.T) {
	tests := []struct {
		name          string
		example, real bool
	}{
		{".env", false, true},
		{"apps/api/.env.local", false, true},
		{".env.production", false, true},
		{".env.example", true, false},
		{".env.sample", true, false},
		{".env.local.example", true, false},
		{"env.example", true, false},
		{"config/prod.env", false, true},
		{".envrc", false, false},
		{"environment.ts", false, false},
	}
	for _, tt := range tests {
		if ex, real := envFileKind(tt.name); ex != tt.example || real != tt.real {
			t.Errorf("envFileKind(%q) = %v, %v", tt.name, ex, real)
		}
	}
}

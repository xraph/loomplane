package deployments

import (
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

func TestDigitalOceanHistoricalRollbackCannotRestoreExternalCredentials(t *testing.T) {
	f, client := newFakeAppPlatform(t)
	cfg := apiConfig()
	cfg.TargetAccountIdentity = "reviewed-account"
	cfg.Artifact = &planning.ReusedArtifact{Revision: "do_dep_7", CommitSHA: doApproved}
	job := doJob(cfg, "main")
	job.Binding = &planning.Binding{ExternalID: "existing-app"}
	f.apps["existing-app"] = map[string]any{"name": "existing-app"}
	f.active["existing-app"] = "current-deployment"
	_, err := newDO(client).Apply(t.Context(), job)
	if err == nil || !strings.Contains(err.Error(), "external") || len(f.rollbacks) != 0 || len(f.specs) != 0 {
		t.Fatalf("historical external access reached provider rollback: error=%v rollback-count=%d", err, len(f.rollbacks))
	}
}

func TestDigitalOceanExternalCredentialsAreRuntimeOnly(t *testing.T) {
	cfg := apiConfig()
	cfg.Env = []planning.EnvLink{{Key: "DATABASE_URL", From: "db", Subject: planning.ResourceSubject}, {Key: "CACHE_URL", From: "cache", Subject: planning.ResourceSubject}, {Key: "AWS_SECRET_ACCESS_KEY", From: "files", Subject: planning.ResourceSubject, Field: "output:AWS_SECRET_ACCESS_KEY"}}
	job := doJob(cfg, "main")
	job.Env = map[string]string{"DATABASE_URL": "postgres://app:secret@db/app", "CACHE_URL": "rediss://:secret@cache:6380", "AWS_SECRET_ACCESS_KEY": "application-key", "NPM_TOKEN": "build-token"}
	job.Secrets = map[string]bool{"DATABASE_URL": true, "CACHE_URL": true, "AWS_SECRET_ACCESS_KEY": true, "NPM_TOKEN": true}
	envs := doAppSpec(job, cfg, "acme/mono", "main").Services[0].Envs
	for _, env := range envs {
		if env.Key == "PORT" {
			continue
		}
		if env.Key == "NPM_TOKEN" {
			if env.Type != "SECRET" || env.Scope != "RUN_AND_BUILD_TIME" {
				t.Fatal("explicit build secret behavior changed")
			}
			continue
		}
		if env.Type != "SECRET" || env.Scope != "RUN_TIME" {
			t.Fatalf("external application credential exposed to builds: key=%s type=%s scope=%s", env.Key, env.Type, env.Scope)
		}
	}
}

func TestNativeBucketLongTermKeysOmitEmptyOptionalToken(t *testing.T) {
	cfg := apiConfig()
	cfg.Env = []planning.EnvLink{{Key: "FILES_AWS_SESSION_TOKEN", From: "files", Subject: planning.ResourceSubject, Field: "output:AWS_SESSION_TOKEN"}, {Key: "FILES_AWS_SECRET_ACCESS_KEY", From: "files", Subject: planning.ResourceSubject, Field: "output:AWS_SECRET_ACCESS_KEY"}}
	job := doJob(cfg, "main")
	job.Env = map[string]string{"FILES_AWS_SESSION_TOKEN": "", "FILES_AWS_SECRET_ACCESS_KEY": "application-secret"}
	job.Secrets = map[string]bool{"FILES_AWS_SESSION_TOKEN": true, "FILES_AWS_SECRET_ACCESS_KEY": true}
	for _, env := range doAppSpec(job, cfg, "acme/mono", "main").Services[0].Envs {
		if env.Key == "FILES_AWS_SESSION_TOKEN" {
			t.Fatal("App Platform emitted an empty optional token secret")
		}
	}
	template, secrets := azureTemplate(job, cfg, "registry/app@sha256:reviewed")
	for _, env := range template.Containers[0].Env {
		if env.Name == "FILES_AWS_SESSION_TOKEN" {
			t.Fatal("Container Apps emitted an empty optional token environment value")
		}
	}
	if len(secrets) != 1 || secrets[0].Value != "application-secret" {
		t.Fatal("Container Apps did not preserve the required application key")
	}
}

func TestNativeExternalTargetIsRecheckedBeforeSecretDelivery(t *testing.T) {
	for _, scenario := range []string{"unchanged", "account", "settings", "id", "org", "status"} {
		t.Run(scenario, func(t *testing.T) {
			conn := &connections.Connection{ID: "native", OrgID: "org", Cloud: connections.Azure, Status: connections.StatusReady, Details: map[string]string{"tenant_id": "tenant", "subscription_id": "sub", "resource_group": "production"}}
			a := planning.Action{Provider: "azure-container-apps", ConnectionID: "native", Service: &planning.ServiceConfig{TargetAccountIdentity: connections.AccountIdentity(conn.Cloud, conn.Account, conn.Details), TargetSettings: planning.ReviewedConnectionSettings(conn.Cloud, conn.Details)}}
			switch scenario {
			case "account":
				conn.Details["subscription_id"] = "another-sub"
			case "settings":
				conn.Details["registry_pull"] = "identity"
			case "id":
				conn.ID = "another-id"
			case "org":
				conn.OrgID = "another-org"
			case "status":
				conn.Status = "error"
			}
			err := checkExternalResourceTarget("org", a, conn)
			if (err == nil) != (scenario == "unchanged") {
				t.Fatalf("target substitution gate: %v", err)
			}
		})
	}
}

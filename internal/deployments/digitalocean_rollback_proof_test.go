package deployments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/planning"
)

// These exercise the provider boundary: a matching recorded release restores
// its retained build, while changed credentials or settings never reach POST.
func doExternalProofJob() (*Job, map[string][]byte) {
	cfg := apiConfig()
	cfg.TargetAccountIdentity = "reviewed-account"
	cfg.Env = []planning.EnvLink{{Key: "DATABASE_URL", From: "db", Subject: planning.ResourceSubject}, {Key: "CACHE_URL", From: "cache", Subject: planning.ResourceSubject}, {Key: "AWS_SECRET_ACCESS_KEY", From: "files", Subject: planning.ResourceSubject}}
	job := doJob(cfg, "main")
	job.Action.ConnectionID = "native"
	job.Deployment.EnvironmentID = "env-production"
	job.Env = map[string]string{"DATABASE_URL": "postgres://database-secret", "CACHE_URL": "rediss://cache-secret", "AWS_SECRET_ACCESS_KEY": "bucket-secret", "BUILD_TOKEN": "build-secret"}
	job.Secrets = map[string]bool{"DATABASE_URL": true, "CACHE_URL": true, "AWS_SECRET_ACCESS_KEY": true, "BUILD_TOKEN": true}
	kept := map[string][]byte{}
	job.Keep = func(_ context.Context, field string, value []byte) error {
		kept[field] = bytes.Clone(value)
		return nil
	}
	job.Recall = func(_ context.Context, field string) ([]byte, error) { return bytes.Clone(kept[field]), nil }
	return job, kept
}

func doSavedRollbackProofs(kept map[string][]byte) int {
	count := 0
	for field := range kept {
		if strings.HasPrefix(field, "app-platform-rollback:") {
			count++
		}
	}
	return count
}

func TestDigitalOceanExternalRollbackRequiresExactRecordedSnapshot(t *testing.T) {
	for _, change := range []string{"unchanged", "token-rotation", "database", "cache", "bucket", "build-secret", "secret-classification", "settings", "app", "account", "connection", "project", "environment", "commit", "historical-spec", "historical-unknown-field", "missing-proof", "vault-read", "marker-cleared", "domain"} {
		t.Run(change, func(t *testing.T) {
			f, client := newFakeAppPlatform(t)
			f.encryptSecrets = true
			job, kept := doExternalProofJob()
			out, err := newDO(client).Apply(t.Context(), job)
			if err != nil {
				t.Fatal(err)
			}
			if doSavedRollbackProofs(kept) != 1 {
				t.Fatalf("recorded %d proofs, want one", doSavedRollbackProofs(kept))
			}
			for _, raw := range kept {
				for _, secret := range []string{"database-secret", "cache-secret", "bucket-secret", "build-secret"} {
					if bytes.Contains(raw, []byte(secret)) {
						t.Fatal("proof retained plaintext secret")
					}
				}
			}
			job.Binding = &planning.Binding{ExternalID: out.ExternalID}
			job.Action.Service.Artifact = &planning.ReusedArtifact{Revision: out.Revision, CommitSHA: doApproved}
			job.Resume = nil
			switch change {
			case "token-rotation":
				job.Credential = []byte("rotated-provider-token")
			case "database":
				job.Env["DATABASE_URL"] = "rotated-database-secret"
			case "cache":
				job.Env["CACHE_URL"] = "rotated-cache-secret"
			case "bucket":
				job.Env["AWS_SECRET_ACCESS_KEY"] = "rotated-bucket-secret"
			case "build-secret":
				job.Env["BUILD_TOKEN"] = "rotated-build-secret"
			case "secret-classification":
				job.Secrets["DATABASE_URL"] = false
			case "settings":
				job.Action.Service.Port = 9090
			case "app":
				job.Binding.ExternalID = "another-app"
			case "account":
				job.Action.Service.TargetAccountIdentity = "another-account"
			case "connection":
				job.Action.ConnectionID = "another-connection"
			case "project":
				job.Deployment.ProjectID = "another-project"
			case "environment":
				job.Deployment.EnvironmentID = "another-environment"
			case "commit":
				f.tip = "ffffffffffffffffffffffffffffffffffffffff"
			case "historical-spec":
				f.deployments[out.Revision]["region"] = "fra"
			case "historical-unknown-field":
				f.deployments[out.Revision]["ingress"] = map[string]any{"rules": []any{}}
			case "missing-proof":
				clear(kept)
			case "vault-read":
				job.Recall = func(context.Context, string) ([]byte, error) {
					return nil, errors.New("vault unavailable: bucket-secret")
				}
			case "marker-cleared":
				job.Action.Service.TargetAccountIdentity = ""
				job.Action.Service.Env = nil
				job.Env["DATABASE_URL"] = "rotated-database-secret"
			case "domain":
				f.apps[out.ExternalID] = map[string]any{"name": "current", "domains": []any{map[string]any{"domain": "new.example.com", "type": "ALIAS"}}}
			}
			before := len(f.specs)
			rolled, err := newDO(client).Apply(t.Context(), job)
			if change == "unchanged" || change == "token-rotation" {
				if err != nil || rolled.Revision != "rb" || len(f.rollbacks) != 1 || len(f.created) != 1 {
					t.Fatalf("retained rollback outcome=%+v err=%v rollbacks=%v builds=%v", rolled, err, f.rollbacks, f.created)
				}
				if doSavedRollbackProofs(kept) != 2 {
					t.Fatal("rollback's new revision has no proof")
				}
			} else if err == nil || len(f.rollbacks) != 0 || len(f.specs) != before || len(f.created) != 1 {
				t.Fatalf("changed snapshot reached mutation: err=%v rollbacks=%v", err, f.rollbacks)
			}
			if err != nil {
				for _, secret := range []string{"database-secret", "cache-secret", "bucket-secret", "build-secret"} {
					if strings.Contains(err.Error(), secret) {
						t.Fatal("secret in error")
					}
				}
			}
		})
	}
}

func TestDigitalOceanRollbackProofVaultFailureResumesWithoutAnotherMutation(t *testing.T) {
	f, client := newFakeAppPlatform(t)
	job, kept := doExternalProofJob()
	var checkpoint []byte
	job.Checkpoint = func(b []byte) error { checkpoint = bytes.Clone(b); return nil }
	keep := job.Keep
	job.Keep = func(ctx context.Context, field string, data []byte) error {
		if strings.HasPrefix(field, "app-platform-rollback:") {
			return errors.New("vault unavailable: database-secret")
		}
		return keep(ctx, field, data)
	}
	_, err := newDO(client).Apply(t.Context(), job)
	if err == nil || strings.Contains(err.Error(), "database-secret") || doSavedRollbackProofs(kept) != 0 {
		t.Fatalf("proof write failure: %v", err)
	}
	job.Keep, job.Resume = keep, checkpoint
	if _, err = newDO(client).Apply(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 || doSavedRollbackProofs(kept) != 1 {
		t.Fatalf("retry created=%v proofs=%d", f.created, doSavedRollbackProofs(kept))
	}
}

func TestDigitalOceanRollbackProofRejectsProviderEditsBeforeCapture(t *testing.T) {
	for _, change := range []string{"secret", "ingress", "health-check", "global-env"} {
		t.Run(change, func(t *testing.T) {
			f, client := newFakeAppPlatform(t)
			job, kept := doExternalProofJob()
			f.encryptSecrets = true
			f.alterBuild = func(spec map[string]any) {
				component := spec["services"].([]any)[0].(map[string]any)
				switch change {
				case "secret":
					for _, item := range component["envs"].([]any) {
						env := item.(map[string]any)
						if env["key"] == "DATABASE_URL" {
							env["value"] = "different-provider-ciphertext"
						}
					}
				case "ingress":
					spec["ingress"] = map[string]any{"rules": []any{map[string]any{"component": map[string]any{"name": "api", "rewrite": "/unreviewed"}}}}
				case "health-check":
					component["health_check"] = map[string]any{"http_path": "/unreviewed"}
				case "global-env":
					spec["envs"] = []any{map[string]any{"key": "DATABASE_URL", "value": "unreviewed"}}
				}
			}
			_, err := newDO(client).Apply(t.Context(), job)
			if err == nil {
				t.Fatal("provider spec changed after submission but was certified")
			}
			if doSavedRollbackProofs(kept) != 0 {
				t.Fatal("changed spec has reusable rollback proof")
			}
		})
	}
}

func TestDigitalOceanRollbackProofFailureResumesRecordedRollback(t *testing.T) {
	f, client := newFakeAppPlatform(t)
	job, kept := doExternalProofJob()
	out, err := newDO(client).Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	job.Binding = &planning.Binding{ExternalID: out.ExternalID}
	job.Action.Service.Artifact = &planning.ReusedArtifact{Revision: out.Revision, CommitSHA: doApproved}
	var checkpoint []byte
	job.Checkpoint = func(data []byte) error { checkpoint = bytes.Clone(data); return nil }
	keep := job.Keep
	job.Keep = func(ctx context.Context, field string, data []byte) error {
		if field == "app-platform-rollback:rb" {
			return errors.New("vault unavailable: cache-secret")
		}
		return keep(ctx, field, data)
	}
	if _, err = newDO(client).Apply(t.Context(), job); err == nil || strings.Contains(err.Error(), "cache-secret") {
		t.Fatalf("rollback proof failure: %v", err)
	}
	job.Resume, job.Keep = checkpoint, keep
	if _, err = newDO(client).Apply(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	if len(f.rollbacks) != 1 || doSavedRollbackProofs(kept) != 2 {
		t.Fatalf("replayed rollback: rollbacks=%v proofs=%d", f.rollbacks, doSavedRollbackProofs(kept))
	}
}

func TestDigitalOceanWrongCommitRecoveryVerifiesCurrentExternalAccess(t *testing.T) {
	for _, change := range []string{"unchanged", "rotated", "missing-proof", "binding-app", "binding-revision", "binding-commit", "marker-cleared", "marker-cleared-missing-proof", "empty-prior-null", "empty-prior-object"} {
		t.Run(change, func(t *testing.T) {
			f, client := newFakeAppPlatform(t)
			job, kept := doExternalProofJob()
			out, err := newDO(client).Apply(t.Context(), job)
			if err != nil {
				t.Fatal(err)
			}
			previousConfig, _ := json.Marshal(job.Action.Service)
			job.Binding = &planning.Binding{ExternalID: out.ExternalID, Revision: out.Revision, CommitSHA: doApproved, Config: previousConfig}
			job.CommitSHA = "1111111111111111111111111111111111111111"
			switch change {
			case "rotated":
				job.Env["DATABASE_URL"] = "rotated-database-secret"
			case "missing-proof":
				clear(kept)
			case "binding-app":
				job.Binding.ExternalID = "different-app"
			case "binding-revision":
				job.Binding.Revision = "different-revision"
			case "binding-commit":
				job.Binding.CommitSHA = ""
			case "empty-prior-null", "empty-prior-object":
				job.Action.Service.TargetAccountIdentity = ""
				job.Action.Service.Env = nil
				job.Binding.Config = []byte(`null`)
				if change == "empty-prior-object" {
					job.Binding.Config = []byte(`{}`)
				}
				clear(kept)
			case "marker-cleared", "marker-cleared-missing-proof":
				job.Action.Service.TargetAccountIdentity = ""
				job.Action.Service.Env = nil
				job.Env["DATABASE_URL"] = "rotated-database-secret"
				if change == "marker-cleared-missing-proof" {
					clear(kept)
				}
			}
			var locked bool
			client.Lock = func(ctx context.Context, _ string, fn func(context.Context) error) error {
				locked = true
				defer func() { locked = false }()
				return fn(ctx)
			}
			f.held = func() bool { return locked }
			mm := &digitalocean.CommitMismatchError{Deployment: &digitalocean.Deployment{Phase: digitalocean.PhaseActive}, Built: "ffffffffffffffffffffffffffffffffffffffff", Want: job.CommitSHA}
			err = newDO(client).refuse(t.Context(), "token", job, doState{AppID: out.ExternalID, DeploymentID: "wrong-live", Previous: out.Revision}, mm, "main")
			if change == "unchanged" {
				if len(f.rollbacks) != 1 || !f.locked["POST /v2/apps/"+out.ExternalID+"/rollback"] {
					t.Fatalf("recovery did not verify under app lock: err=%v rollback=%v", err, f.rollbacks)
				}
			} else if len(f.rollbacks) != 0 || err == nil || !strings.Contains(err.Error(), "current attachments") {
				t.Fatalf("unsafe compensation reached rollback: err=%v rollback=%v", err, f.rollbacks)
			}
		})
	}
}

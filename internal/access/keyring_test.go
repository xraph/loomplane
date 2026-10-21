package access

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domain "github.com/xraph/loomplane/pkg/federation"
)

func keyringFixture(t *testing.T) (string, domain.Scope, map[string]any) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	scope := domain.Scope{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}
	record := map[string]any{"org_id": scope.OrgID, "project_id": scope.ProjectID, "environment_id": scope.EnvironmentID, "private_key": base64.StdEncoding.EncodeToString(private), "not_before": time.Now().Add(-time.Minute).UTC(), "not_after": time.Now().Add(time.Hour).UTC()}
	return filepath.Join(t.TempDir(), "keys.json"), scope, record
}
func writeKeyring(t *testing.T, path string, records []map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"version": 1, "keys": records})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}
func TestSigningKeyringScopesAndRestart(t *testing.T) {
	path, scope, record := keyringFixture(t)
	writeKeyring(t, path, []map[string]any{record})
	first, err := LoadSigningKeyring(path, scope.OrgID)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := first.Signer(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := LoadSigningKeyring(path, scope.OrgID)
	if err != nil {
		t.Fatal(err)
	}
	same, err := restarted.Signer(t.Context(), scope)
	if err != nil || signer.Key().ID() != same.Key().ID() {
		t.Fatal("restart regenerated signing authority")
	}
	for _, foreign := range []domain.Scope{{OrgID: "other", ProjectID: scope.ProjectID, EnvironmentID: scope.EnvironmentID}, {OrgID: scope.OrgID, ProjectID: "other", EnvironmentID: scope.EnvironmentID}} {
		if _, err := first.Signer(t.Context(), foreign); err == nil {
			t.Fatal("unconfigured scope received signing authority")
		}
	}
}
func TestSigningKeyringRejectsUnsafeConfiguration(t *testing.T) {
	for _, change := range []string{"foreign", "duplicate", "expired", "future", "key", "permissions", "symlink", "unknown", "trailing", "oversized"} {
		t.Run(change, func(t *testing.T) {
			path, scope, record := keyringFixture(t)
			secret := record["private_key"].(string)
			records := []map[string]any{record}
			switch change {
			case "foreign":
				record["org_id"] = "other"
			case "duplicate":
				records = append(records, record)
			case "expired":
				record["not_after"] = time.Now().Add(-time.Second).UTC()
			case "future":
				record["not_before"] = time.Now().Add(time.Minute).UTC()
			case "key":
				record["private_key"] = "secret-invalid-key"
			case "unknown":
				record["unknown"] = "value"
			}
			writeKeyring(t, path, records)
			switch change {
			case "permissions":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				link := path + ".link"
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			case "trailing":
				file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = file.WriteString(" {}"); err != nil {
					t.Fatal(err)
				}
				if err = file.Close(); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				if err := os.WriteFile(path, []byte(strings.Repeat(" ", 1<<20)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := LoadSigningKeyring(path, scope.OrgID)
			if err == nil {
				t.Fatal("unsafe keyring accepted")
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "secret-invalid-key") {
				t.Fatal("key material in error")
			}
		})
	}
}

func TestSigningKeyringRechecksExpiryAndCancellation(t *testing.T) {
	path, scope, record := keyringFixture(t)
	writeKeyring(t, path, []map[string]any{record})
	ring, err := LoadSigningKeyring(path, scope.OrgID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ring.Signer(ctx, scope); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled caller received signing authority", err)
	}
	if err := ring.Health(t.Context()); err != nil {
		t.Fatal(err)
	}
	ring.signers[scope].key.NotAfter = time.Now().Add(-time.Second)
	if err := ring.Health(t.Context()); err == nil {
		t.Fatal("expired keyring healthy")
	}
	if _, err := ring.Signer(t.Context(), scope); err == nil {
		t.Fatal("expired key remained available")
	}
}

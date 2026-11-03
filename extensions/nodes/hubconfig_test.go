package nodes

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/config"
)

const testSecretKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

// Every replica must derive the same secrets from the shared secret_key, or a
// challenge issued by one fails on another and hops are refused. These are
// HKDF-SHA256 with no salt, computed independently of this code.
func TestHubConfigDerivesPinnedSecrets(t *testing.T) {
	hc, err := hubConfig(config.Config{SecretKey: testSecretKey, PublicURL: publicURL}, "host:1")
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(hc.HopSecret); got != "04b7d95cc4fc2b169687e49732fa18d957f76fff6daf86db442c787072b84a78" {
		t.Errorf("hop secret %s", got)
	}
	if got := hex.EncodeToString(hc.ChallengeSecret); got != "04b81ad7c4c83b183800c87af662f495c8da7bfea41fdda888f2b2a661807ba9" {
		t.Errorf("challenge secret %s", got)
	}
	if hc.Audience != publicURL || hc.Replica != "host:1" || hc.HopRootCAs != nil {
		t.Errorf("config %+v", hc)
	}
}

func TestHubConfigRefusesAnEmptySecretKey(t *testing.T) {
	for _, key := range []string{"", "not base64!"} {
		if _, err := hubConfig(config.Config{SecretKey: key}, "r"); err == nil {
			t.Errorf("secret key %q was accepted", key)
		}
	}
}

func TestHubConfigCAFile(t *testing.T) {
	cfg := config.Config{SecretKey: testSecretKey}
	dir := t.TempDir()
	cfg.Nodes.HopCAFile = filepath.Join(dir, "missing.pem")
	if _, err := hubConfig(cfg, "r"); err == nil || !strings.Contains(err.Error(), "hop_ca_file") {
		t.Errorf("unreadable: %v", err)
	}
	empty := filepath.Join(dir, "empty.pem")
	if err := os.WriteFile(empty, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Nodes.HopCAFile = empty
	if _, err := hubConfig(cfg, "r"); err == nil || !strings.Contains(err.Error(), "no PEM certificate") {
		t.Errorf("no certificate: %v", err)
	}
	// A bad CA file fails Register, not the platform's construction.
	e := New(cfg, "r")
	if e.hubErr == nil {
		t.Error("New dropped the CA file error")
	}
}

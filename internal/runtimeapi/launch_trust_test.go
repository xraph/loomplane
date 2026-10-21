package runtimeapi

import (
	"os"
	"testing"

	domain "github.com/xraph/loomplane/pkg/federation"
)

func TestRuntimeLaunchTrustUsesRunningScopedKey(t *testing.T) {
	runtime, _ := lifecycleFixture(t)
	scope := runtimeAgent(t).Scope
	if _, err := runtime.PublicSigningKey(t.Context(), scope); err == nil {
		t.Fatal("stopped runtime delivered trust")
	}
	if err := runtime.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	key, err := runtime.PublicSigningKey(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	original := key.ID()
	if removeErr := os.Remove(runtime.cfg.SigningKeysFile); removeErr != nil {
		t.Fatal(removeErr)
	}
	key.PublicKey[0] ^= 0xff
	fresh, err := runtime.PublicSigningKey(t.Context(), scope)
	if err != nil || fresh.ID() != original {
		t.Fatal("returned public key aliases live signer", err)
	}
	for _, foreign := range []domain.Scope{{OrgID: "foreign", ProjectID: scope.ProjectID, EnvironmentID: scope.EnvironmentID}, {OrgID: scope.OrgID, ProjectID: "foreign", EnvironmentID: scope.EnvironmentID}} {
		if _, err := runtime.PublicSigningKey(t.Context(), foreign); err == nil {
			t.Fatal("unconfigured scope acquired signing trust")
		}
	}
	if err := runtime.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.PublicSigningKey(t.Context(), scope); err == nil {
		t.Fatal("stopped listener returned launch trust")
	}
}

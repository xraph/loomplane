package connections

import (
	"testing"

	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/azure/azuretest"
)

// A principal without what only a canary needs is complete: every deploy
// still works through it, and the connection says what a canary would
// lack.
func TestAzureCheckKeepsCanaryOnlyActionsOutOfMissing(t *testing.T) {
	f := azuretest.New(t)
	f.NotActions = append(f.NotActions, "Microsoft.App/containerApps/revisions/deactivate/action", "Microsoft.App/containerApps/listSecrets/action")
	c, err := AzureChecker{Client: azure.New(f.URL())}.Check(t.Context(), []byte(azuretest.Secret), azuretest.Settings())
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Missing) != 0 {
		t.Fatalf("missing %v", c.Missing)
	}
	if got := c.Details[DetailCanaryMissing]; got != "Microsoft.App/containerApps/revisions/deactivate/action,Microsoft.App/containerApps/listSecrets/action" {
		t.Fatalf("canary missing %q", got)
	}
	conn := &Connection{}
	apply(conn, c, conn.UpdatedAt)
	if conn.Status != StatusReady {
		t.Fatalf("status %s", conn.Status)
	}

	// A Contributor has them all.
	f.Lock()
	f.NotActions = f.NotActions[:2]
	f.Unlock()
	c, err = AzureChecker{Client: azure.New(f.URL())}.Check(t.Context(), []byte(azuretest.Secret), azuretest.Settings())
	if err != nil {
		t.Fatal(err)
	}
	if _, has := c.Details[DetailCanaryMissing]; has || len(c.Missing) != 0 {
		t.Fatalf("contributor: %+v", c)
	}
}

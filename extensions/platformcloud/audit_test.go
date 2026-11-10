package platformcloud

import (
	"context"
	"testing"

	"github.com/xraph/chronicle"
	"github.com/xraph/chronicle/audit"
	"github.com/xraph/chronicle/store"
	"github.com/xraph/chronicle/store/memory"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/platformadmin"
)

// A platform owner's change goes to the staff stream, not the app level
// where customers' account events are.
func TestChronicleAuditorRecordsInThePlatformStream(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	c, err := chronicle.New(chronicle.WithStore(store.NewAdapter(mem)))
	if err != nil {
		t.Fatal(err)
	}
	a := chronicleAuditor{chronicle: c, appID: "aapp_test", log: forge.NewNoopLogger()}
	a.Record(ctx, "platform_cloud.region.create", "ausr_owner", "region", "r1", "success", map[string]string{"name": "eu"})
	res, err := mem.Query(ctx, &audit.Query{AppID: "aapp_test"})
	if err != nil || len(res.Events) != 1 {
		t.Fatalf("query: %v, %d events", err, len(res.Events))
	}
	if got := res.Events[0].TenantID; got != platformadmin.AuditTenant {
		t.Fatalf("recorded under tenant %q, want %q", got, platformadmin.AuditTenant)
	}
}

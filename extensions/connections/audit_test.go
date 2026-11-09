package connections

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

// A sign-in app change is a platform user's, so it goes to the staff
// stream.
func TestRecordAppRecordsInThePlatformStream(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	c, err := chronicle.New(chronicle.WithStore(store.NewAdapter(mem)))
	if err != nil {
		t.Fatal(err)
	}
	auditor{chronicle: c, appID: "aapp_test", log: forge.NewNoopLogger()}.RecordApp(ctx, "cloud_signin_app.set", "ausr_staff", "vercel", "client-1")
	res, err := mem.Query(ctx, &audit.Query{AppID: "aapp_test"})
	if err != nil || len(res.Events) != 1 {
		t.Fatalf("query: %v, %d events", err, len(res.Events))
	}
	if got := res.Events[0].TenantID; got != platformadmin.AuditTenant {
		t.Fatalf("recorded under tenant %q, want %q", got, platformadmin.AuditTenant)
	}
}

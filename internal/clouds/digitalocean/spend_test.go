package digitalocean

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/metrics"
)

func balance(usage any) func(http.ResponseWriter, *http.Request, map[string]any) {
	return func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"month_to_date_balance": "23.44", "account_balance": "12.23",
			"month_to_date_usage": usage, "generated_at": "2026-10-24T15:01:12Z"})
	}
}

func TestMonthToDate(t *testing.T) {
	f, c := newFake(t)
	f.on("GET /v2/customers/my/balance", balance("11.21"))
	got, err := c.MonthToDate(context.Background(), "dop_v1_good", time.Now())
	if err != nil || !reflect.DeepEqual(got, &metrics.Spend{Currency: "USD", Total: 11.21}) {
		t.Fatalf("%+v, %v", got, err)
	}
	if !reflect.DeepEqual(f.calls, []string{"GET /v2/customers/my/balance"}) {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestMonthToDate_nothingYet(t *testing.T) {
	f, c := newFake(t)
	f.on("GET /v2/customers/my/balance", balance("0.00"))
	got, err := c.MonthToDate(context.Background(), "dop_v1_good", time.Now())
	if err != nil || got.Total != 0 || got.Currency != "USD" || got.Days != nil {
		t.Fatalf("%+v, %v", got, err)
	}
}

func TestMonthToDate_anUnreadableAmountIsAnError(t *testing.T) {
	for _, usage := range []any{"eleven", "", nil} {
		f, c := newFake(t)
		f.on("GET /v2/customers/my/balance", balance(usage))
		if got, err := c.MonthToDate(context.Background(), "dop_v1_good", time.Now()); err == nil {
			t.Errorf("%v: %+v", usage, got)
		}
	}
}

func TestMonthToDate_forbiddenIsUnavailable(t *testing.T) {
	f, c := newFake(t)
	f.on("GET /v2/customers/my/balance", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, http.StatusForbidden, map[string]any{"id": "Forbidden", "message": "You are not authorized to perform this operation"})
	})
	_, err := c.MonthToDate(context.Background(), "dop_v1_good", time.Now())
	// A token with every scope is still refused when the person who made
	// it can't see billing, so the reason says what DigitalOcean said and
	// names the role, not just the scope.
	want := "DigitalOcean refused the balance: You are not authorized to perform this operation. " +
		"A token has its creator's role, so make it as a team owner, admin or biller, with billing:read."
	if reason, ok := metrics.IsUnavailable(err); !ok || reason != want {
		t.Fatalf("%v", err)
	}
}

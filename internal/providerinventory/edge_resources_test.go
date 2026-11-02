package providerinventory

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/connections"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/clouds/vercel"
)

func TestEdgeProvidersRequireInstalledInventoryClients(t *testing.T) {
	c := &Clients{Cloudflare: cloudflare.New("", false), Vercel: vercel.New("")}
	got := c.SupportedProviders()
	if !c.Supports("cloudflare") || !c.Supports("vercel") || len(got) != 2 {
		t.Fatalf("edge resource readers not installed: %v", got)
	}
}

func TestCloudflarePartialInventoryUsesEachNativeScope(t *testing.T) {
	account := strings.Repeat("a", 32)
	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/accounts/"+account {
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + account + `","name":"test"}}`))
			return
		}
		if r.URL.Path == "/accounts/"+account+"/d1/database" {
			reads++
			_, _ = w.Write([]byte(`{"success":true,"result":[{"uuid":"11111111-1111-1111-1111-111111111111","name":"application"}],"result_info":{"page":1,"total_pages":1}}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10000}]}`))
	}))
	defer srv.Close()
	conn := &connections.Connection{Cloud: "cloudflare", Details: map[string]string{"account_id": account}}
	got, err := (&Clients{Cloudflare: cloudflare.New(srv.URL, false)}).Inventory(t.Context(), conn, []byte("resource-token"))
	if err != nil || got.Complete || reads != 1 || len(got.Resources) != 1 || got.Resources[0].Kind != "cloudflare-d1" || got.Resources[0].Scope != "accounts/"+account+"/d1" {
		t.Fatalf("authorized D1 scope was lost with unrelated denied products: reads=%d result=%+v err=%v", reads, got, err)
	}
}

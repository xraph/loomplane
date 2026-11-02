package connections

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/clouds/oauth"
)

func TestCloudflareStorageOnlyConnectionDoesNotProbeDNS(t *testing.T) {
	account := strings.Repeat("a", 32)
	denied := false
	accountDenied := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/accounts/" + account + "/tokens/verify":
			fmt.Fprint(w, `{"success":true,"result":{"id":"tok","status":"active"}}`)
		case "/accounts/" + account:
			if accountDenied {
				w.WriteHeader(403)
				fmt.Fprint(w, `{"success":false}`)
				return
			}
			fmt.Fprintf(w, `{"success":true,"result":{"id":%q,"name":"Storage"}}`, account)
		case "/accounts/" + account + "/r2/buckets":
			if denied {
				w.WriteHeader(403)
				fmt.Fprint(w, `{"success":false}`)
				return
			}
			fmt.Fprint(w, `{"success":true,"result":{"buckets":[]}}`)
		default:
			t.Errorf("storage connection probed unrelated API %s", r.URL.Path)
			w.WriteHeader(403)
		}
	}))
	defer api.Close()
	checker := CloudflareChecker{Client: cloudflare.New(api.URL, false)}
	got, err := checker.Check(t.Context(), []byte("storage-token"), map[string]string{"account_id": account, "services": "r2"})
	if err != nil || len(got.Missing) != 0 || got.Details["services"] != "r2" || got.Details["r2_read"] != "granted" || got.Details["dns_write"] != "unchecked" {
		t.Fatalf("storage check: %+v %v", got, err)
	}
	grant, _ := json.Marshal(oauth.Grant{AccessToken: "storage-token", Account: map[string]string{"account_id": account}})
	got, err = checker.Check(t.Context(), grant, map[string]string{"account_id": account, "services": "r2"})
	if err != nil || len(got.Missing) != 0 {
		t.Fatalf("OAuth storage check: %+v %v", got, err)
	}
	accountDenied = true
	got, err = checker.Check(t.Context(), []byte("storage-token"), map[string]string{"account_id": account, "services": "r2"})
	if err != nil || len(got.Missing) != 1 || got.Missing[0] != "Account Settings Read" {
		t.Fatalf("inventory needs account metadata: %+v %v", got, err)
	}
	accountDenied = false
	denied = true
	got, err = checker.Check(t.Context(), grant, map[string]string{"account_id": account, "services": "r2"})
	if err != nil || len(got.Missing) != 1 || got.Missing[0] != "R2 Storage Read" {
		t.Fatalf("denied storage check: %+v %v", got, err)
	}
}

func TestCloudflareServiceSelectionRejectsUnknownAndPreservesSignIn(t *testing.T) {
	account := strings.Repeat("a", 32)
	settings, err := signInSettings(Cloudflare, map[string]string{"account_id": account, "services": "r2"})
	if err != nil || settings["services"] != "r2" {
		t.Fatalf("lost R2 selection: %v %v", settings, err)
	}
	if _, err = signInSettings(Cloudflare, map[string]string{"account_id": account, "services": "admin"}); err == nil {
		t.Fatal("unknown services accepted")
	}
}

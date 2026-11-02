package notifications

import (
	"testing"
	"time"
)

func TestRenewalNoticeCopy(t *testing.T) {
	renews := time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)
	title, body, subject := renewalCopy("acme.dev", renews, "USD", "10.11")
	if title != "acme.dev renews on Nov 30, 2026" || subject != "Loomplane: acme.dev renews on Nov 30, 2026" {
		t.Fatalf("title=%q subject=%q", title, subject)
	}
	if body != "Cloudflare renews acme.dev on Nov 30, 2026 for USD 10.11. Turn auto-renew off before then if you don't want to keep it." {
		t.Fatalf("body with a price: %q", body)
	}
	for _, price := range [][2]string{{"", ""}, {"USD", ""}, {"", "10.11"}} {
		_, body, _ = renewalCopy("acme.dev", renews, price[0], price[1])
		if body != "Cloudflare renews acme.dev on Nov 30, 2026. Loomplane doesn't know the renewal price. Turn auto-renew off before then if you don't want to keep it." {
			t.Fatalf("body without a price %v: %q", price, body)
		}
	}
}

func TestRegistrationResultCopy(t *testing.T) {
	dashboard := "https://dash.cloudflare.com/account/domains/registrations"
	for _, tc := range []struct{ status, message, title, body string }{
		{"succeeded", "", "acme.dev is registered", "acme.dev is registered in your Cloudflare account, and Loomplane manages its DNS."},
		{"price_changed", "Cloudflare now quotes USD 12.00 for acme.dev.", "acme.dev wasn't registered", "The price changed before purchase. Nothing was charged. Register it again to see the new price."},
		{"failed", "Cloudflare didn't register acme.dev (registry_refused)", "acme.dev wasn't registered", "Cloudflare declined the registration: Cloudflare didn't register acme.dev (registry_refused)"},
		{"action_required", dashboard, "acme.dev wasn't registered", "Finish this in your Cloudflare dashboard. " + dashboard},
		{"blocked", dashboard, "acme.dev wasn't registered", "Finish this in your Cloudflare dashboard. " + dashboard},
		{"unconfirmed", "Cloudflare has no record of this registration yet.", "acme.dev wasn't registered", "Cloudflare has no record of this registration yet."},
	} {
		title, body := registrationCopy("acme.dev", tc.status, tc.message)
		if title != tc.title || body != tc.body {
			t.Fatalf("%s: title=%q body=%q", tc.status, title, body)
		}
	}
}

func TestDomainNoticeIDs(t *testing.T) {
	n := RenewalNotice{RegistrationID: "reg_1", RenewsOn: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)}
	if got := renewalKey(n); got != "renewal:reg_1:2026-11-30" {
		t.Fatal(got)
	}
	if got := registrationKey(RegistrationResultNotice{RegistrationID: "reg_1", Status: "succeeded"}); got != "registration:reg_1:succeeded" {
		t.Fatal(got)
	}
}

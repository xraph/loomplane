package notifications

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/herald"
	"github.com/xraph/herald/provider"
	heraldmem "github.com/xraph/herald/store/memory"

	"github.com/xraph/loomplane/internal/config"
)

func hostMailService(t *testing.T) (*Service, *capturingDriver, *heraldmem.Store) {
	t.Helper()
	ctx := context.Background()
	store := heraldmem.New()
	sink := &capturingDriver{}
	engine, err := herald.New(herald.WithStore(privateHeraldStore{Store: store}), herald.WithDriver(sink))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{PublicURL: "https://loom.example", Identity: config.IdentityConfig{AppID: "app"}, Notifications: config.NotificationsConfig{SMTPHost: "localhost"}}
	svc := &Service{cfg: cfg, herald: engine, mailStore: privateHeraldStore{Store: store}}
	// Seeding twice must not duplicate a template.
	for range 2 {
		if err = svc.seedTemplates(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err = engine.CreateProvider(ctx, &provider.Provider{AppID: "app", Name: "test", Channel: "email", Driver: "capture", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return svc, sink, store
}

func TestSeedTemplatesAddsTheHostCertificateEmailOnce(t *testing.T) {
	_, _, store := hostMailService(t)
	ctx := context.Background()
	tmpl, err := store.GetTemplateBySlug(ctx, "app", hostCertificateSlug, "email")
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.Category != "system" || !tmpl.Enabled {
		t.Fatalf("template %+v", tmpl)
	}
	versions, err := store.ListVersions(ctx, tmpl.ID)
	if err != nil || len(versions) != 1 {
		t.Fatalf("versions: %d, %v", len(versions), err)
	}
	all, err := store.ListTemplates(ctx, "app")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, tm := range all {
		seen[tm.Slug]++
	}
	if seen[hostCertificateSlug] != 1 || seen[waitlistApprovedSlug] != 1 {
		t.Fatalf("own templates seeded %d and %d times", seen[hostCertificateSlug], seen[waitlistApprovedSlug])
	}
}

func TestSendHostCertificateExpiring(t *testing.T) {
	svc, sink, _ := hostMailService(t)
	n := HostCertificateExpiry{HostID: "pchost_1", HostName: "<b>fc-1</b>", ExpiresAt: time.Now().Add(7*24*time.Hour + time.Hour), DaysLeft: 7}
	if err := svc.SendHostCertificateExpiring(context.Background(), "owner@example.com", n); err != nil {
		t.Fatal(err)
	}
	if len(sink.got) != 1 {
		t.Fatalf("sent %d messages", len(sink.got))
	}
	m := sink.got[0]
	if m.To != "owner@example.com" {
		t.Fatalf("to %v", m.To)
	}
	if m.Subject != "A Platform Cloud host certificate expires in 7 days" {
		t.Fatalf("subject %q", m.Subject)
	}
	if !strings.Contains(m.Text, "host <b>fc-1</b> (pchost_1)") || !strings.Contains(m.Text, "https://loom.example/platform/cloud") {
		t.Fatalf("text:\n%s", m.Text)
	}
	// The host name is the agent's, so the HTML escapes it.
	if strings.Contains(m.HTML, "<b>fc-1</b>") || !strings.Contains(m.HTML, "&lt;b&gt;fc-1&lt;/b&gt;") {
		t.Fatalf("html does not escape the host name:\n%s", m.HTML)
	}
}

func TestSendHostCertificateExpiringRefusesBadInput(t *testing.T) {
	svc, sink, _ := hostMailService(t)
	ctx := context.Background()
	for _, to := range []string{"", "not an address", "Ada <ada@example.com>", "ada@example.com\r\nBcc: x@example.com"} {
		if err := svc.SendHostCertificateExpiring(ctx, to, HostCertificateExpiry{HostID: "h", HostName: "h"}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: %v", to, err)
		}
	}
	if len(sink.got) != 0 {
		t.Fatalf("sent %d messages", len(sink.got))
	}
	off := &Service{cfg: config.Config{PublicURL: "https://loom.example"}}
	if err := off.SendHostCertificateExpiring(ctx, "ada@example.com", HostCertificateExpiry{}); !errors.Is(err, ErrMailDisabled) {
		t.Fatalf("mail off: %v", err)
	}
}

func TestHostCertificateDataWordsEachStage(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		n        HostCertificateExpiry
		headline string
		action   string
	}{
		{"90 days", HostCertificateExpiry{ExpiresAt: now.Add(89 * 24 * time.Hour), DaysLeft: 89}, "expires in 89 days", "keeps using its current certificate"},
		{"tomorrow", HostCertificateExpiry{ExpiresAt: now.Add(30 * time.Hour), DaysLeft: 1}, "expires tomorrow", "keeps using"},
		{"today", HostCertificateExpiry{ExpiresAt: now.Add(3 * time.Hour), DaysLeft: 0}, "expires today", "keeps using"},
		{"just expired", HostCertificateExpiry{ExpiresAt: now.Add(-3 * time.Hour), DaysLeft: 0}, "has expired", "as soon as you can"},
		{"rotating", HostCertificateExpiry{ExpiresAt: now.Add(6 * 24 * time.Hour), DaysLeft: 6, Rotating: true}, "expires in 6 days", "already waiting"},
	}
	for _, c := range cases {
		d := hostCertificateData(c.n, "https://loom.example/platform/cloud", now)
		if !strings.Contains(d["headline"].(string), c.headline) || !strings.Contains(d["action"].(string), c.action) {
			t.Fatalf("%s: %q / %q", c.name, d["headline"], d["action"])
		}
		if strings.Contains(d["headline"].(string), "—") || strings.Contains(d["action"].(string), "—") {
			t.Fatalf("%s: em dash in the copy", c.name)
		}
	}
}

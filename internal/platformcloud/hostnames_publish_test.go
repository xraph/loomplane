//go:build integration

package platformcloud

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/instance"
	"github.com/xraph/loomplane/pkg/provider"
)

// fakeDNS is the platform DNS provider: it keeps what it was asked to write.
type fakeDNS struct {
	mu      sync.Mutex
	n       int
	upserts []provider.DNSRecord
	priors  []*provider.DNSRecord
	deletes []provider.DNSRecord
	zones   []DNSZone
}

func (d *fakeDNS) Upsert(_ context.Context, zone DNSZone, record provider.DNSRecord, expected *provider.DNSRecord) (provider.DNSRecord, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.n++
	d.zones = append(d.zones, zone)
	d.upserts = append(d.upserts, record)
	d.priors = append(d.priors, expected)
	record.ID, record.ZoneID = "rec_"+strings.Repeat("x", d.n), "zone_1"
	return record, nil
}

func (d *fakeDNS) Delete(_ context.Context, zone DNSZone, expected provider.DNSRecord) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.zones = append(d.zones, zone)
	d.deletes = append(d.deletes, expected)
	return nil
}

// A service's generated name points at its host's ingress address when it
// first runs, follows it when it moves, and goes when the service does.
func TestGeneratedHostnamePublishedAndRemoved(t *testing.T) {
	e, h := readyHost(t)
	ctx := t.Context()
	e.svc.settings = fixedSettings{instance.PlatformCloudSettings{Apex: "apps.example.com", DNS: instance.PlatformDNSSettings{Provider: "cloudflare", Zone: "example.com"}}}
	dns := &fakeDNS{}
	e.svc.WithDNS(dns)
	if err := e.svc.SetDNSSecret(ctx, "owner", "acct_1", "", "dns-token"); err != nil {
		t.Fatal(err)
	}

	c, _, p, err := e.svc.Session(ctx, "org_a", "prj_1", "env_1", "inst_web", webRequest())
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err = e.svc.Running(ctx, "org_a", p.ID, "inst_web", 1); err != nil {
		t.Fatal(err)
	}
	name, err := e.svc.Publish(ctx, "org_a", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, "web-") || !strings.HasSuffix(name, ".apps.example.com") {
		t.Fatalf("hostname %q", name)
	}
	relative := strings.TrimSuffix(name, ".example.com")
	if len(dns.upserts) != 1 || dns.upserts[0].Name != relative || dns.upserts[0].Type != "A" || dns.upserts[0].Value != "203.0.113.7" || dns.priors[0] != nil {
		t.Fatalf("first upsert %+v prior %+v", dns.upserts, dns.priors)
	}
	if z := dns.zones[0]; z.Provider != "cloudflare" || z.Zone != "example.com" || z.Credential.Token != "dns-token" || z.Credential.AccountID != "acct_1" {
		t.Fatalf("zone %+v", z)
	}
	// Running it again on the same host writes nothing.
	if again, againErr := e.svc.Publish(ctx, "org_a", p.ID); againErr != nil || again != name || len(dns.upserts) != 1 {
		t.Fatalf("again: %q %v, %d upserts", again, againErr, len(dns.upserts))
	}

	// The service moves to another host in the region.
	other := &Host{ID: "host_2", RegionID: h.RegionID, Endpoint: "https://192.0.2.1:7443", HostID: "hw-2", Status: "ready",
		Evidence: Evidence{IngressAddress: "203.0.113.8"}, Allotment: h.Allotment}
	if err = e.store.CreateHost(ctx, other); err != nil {
		t.Fatal(err)
	}
	moved := "plc_moved"
	if _, err = e.pg.Exec(ctx, `UPDATE loomplane_platform_placements SET state='released' WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.pg.Exec(ctx, `INSERT INTO loomplane_platform_placements(id,org_id,project_id,environment_id,resource,kind,region_id,host_id,vcpus,memory_mib,disk_mib,state,instance_id,created_at,updated_at)
VALUES($1,'org_a','prj_1','env_1','web','service',$2,'host_2',1,512,4096,'running','inst_web',now(),now())`, moved, h.RegionID); err != nil {
		t.Fatal(err)
	}
	if again, againErr := e.svc.Publish(ctx, "org_a", moved); againErr != nil || again != name {
		t.Fatalf("after the move: %q %v", again, againErr)
	}
	if len(dns.upserts) != 2 || dns.upserts[1].Value != "203.0.113.8" || dns.upserts[1].Name != relative || dns.priors[1] == nil || dns.priors[1].ID != "rec_x" {
		t.Fatalf("move upsert %+v prior %+v", dns.upserts, dns.priors)
	}
	// What the move left behind goes without touching the name.
	if err = e.svc.Removed(ctx, "org_a", p.ID); err != nil {
		t.Fatal(err)
	}
	if len(dns.deletes) != 0 {
		t.Fatalf("a move's cleanup deleted %+v", dns.deletes)
	}

	// Releasing the service deletes its record, and its name with it.
	if err = e.svc.Removed(ctx, "org_a", moved); err != nil {
		t.Fatal(err)
	}
	if len(dns.deletes) != 1 || dns.deletes[0].ID != "rec_xx" || dns.deletes[0].Value != "203.0.113.8" {
		t.Fatalf("deletes %+v", dns.deletes)
	}
	if _, err = e.svc.hostnameOf(ctx, "org_a", "env_1", "web"); err == nil {
		t.Fatal("the hostname outlived its service")
	}
	// Another organization can't publish or unpublish it.
	if _, err = e.svc.Publish(ctx, "org_b", moved); err == nil {
		t.Fatal("published another organization's placement")
	}
}

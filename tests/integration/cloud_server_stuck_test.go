//go:build integration

package integration

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/pkg/provider"
)

// A managed server whose create failed after the droplet existed, never
// enrolled, and was then drained, can still be deleted.
func TestCloudServerFailedCreateCanBeDeleted(t *testing.T) {
	for _, drain := range []bool{false, true} {
		name := "no drain"
		if drain {
			name = "drained"
		}
		t.Run(name, func(t *testing.T) {
			f := newInfraFixture(t)
			o := f.create(t)
			o = f.reconcile(t, o)
			if o.Status != "waiting" || o.ResourceID == "" {
				t.Fatalf("after create: %+v", o)
			}
			// Enrollment never comes; the bootstrap window closes.
			o.Status = "failed"
			o.BootstrapExpiresAt = time.Now().Add(-time.Minute)
			if err := f.store.SaveOperation(t.Context(), o); err != nil {
				t.Fatal(err)
			}
			server, err := f.store.GetServer(t.Context(), "org", "cloud", o.ServerID)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("server before: status=%s revision=%d", server.Status, server.Revision)
			if drain {
				review, derr := f.svc.PreviewDrain(t.Context(), f.p, "cloud", server.ID)
				if derr != nil {
					t.Fatalf("preview drain: %v", derr)
				}
				if server, derr = f.svc.ApplyDrain(t.Context(), f.p, "cloud", server.ID, cloudinfra.ApplyInput{Review: review.Review}); derr != nil {
					t.Fatalf("apply drain: %v", derr)
				}
				t.Logf("server drained: status=%s revision=%d", server.Status, server.Revision)
			}
			// The droplet is deleted by hand.
			f.provider.remote.ID = ""
			preview, err := f.svc.PreviewServer(t.Context(), f.p, "cloud", cloudinfra.ServerInput{Kind: "delete", ServerID: server.ID, CloudRevision: 1, ServerRevision: server.Revision})
			if err != nil {
				t.Fatalf("preview delete: %v", err)
			}
			if _, err = f.svc.ApplyServerOperation(t.Context(), f.p, "cloud", preview.ID, cloudinfra.ApplyInput{Review: preview.Review}); err != nil {
				t.Fatalf("apply delete: %v", err)
			}
			preview = f.reconcile(t, preview)
			if preview.Status != "succeeded" {
				t.Fatalf("delete: %+v", preview)
			}
			// The failed create was taken over, and reads back as cancelled.
			created, err := f.store.OperationByID(t.Context(), "org", o.ID)
			if err != nil || created.Status != "cancelled" || !strings.Contains(created.Message, "deletion was accepted") || created.UpdatedAt.IsZero() {
				t.Fatalf("the failed create: %+v %v", created, err)
			}
			if _, err = f.svc.RetryOperation(t.Context(), f.p, "cloud", o.ID); !errors.Is(err, cloudinfra.ErrConflict) {
				t.Fatalf("retrying a superseded create: %v", err)
			}
		})
	}
}

// An operation that's still waiting keeps its server: a delete is refused
// and says so, and nothing is cancelled.
func TestCloudServerOpenOperationStillBlocksDelete(t *testing.T) {
	f := newInfraFixture(t)
	o := f.create(t)
	o = f.reconcile(t, o)
	if o.Status != "waiting" {
		t.Fatalf("after create: %+v", o)
	}
	server, err := f.store.GetServer(t.Context(), "org", "cloud", o.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.svc.PreviewServer(t.Context(), f.p, "cloud", cloudinfra.ServerInput{Kind: "delete", ServerID: server.ID, CloudRevision: 1, ServerRevision: server.Revision})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.ApplyServerOperation(t.Context(), f.p, "cloud", preview.ID, cloudinfra.ApplyInput{Review: preview.Review})
	if !errors.Is(err, cloudinfra.ErrServerOperationOpen) || !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("delete over a waiting create: %v", err)
	}
	if o, err = f.store.OperationByID(t.Context(), "org", o.ID); err != nil || o.Status != "waiting" {
		t.Fatalf("the waiting create: %+v %v", o, err)
	}
}

// A failure's cause reaches the log, since the operation only shows a
// generic message.
func TestCloudServerFailureCauseIsLogged(t *testing.T) {
	f := newInfraFixture(t)
	var logged []string
	f.svc.Warn = func(msg string, err error) { logged = append(logged, msg+": "+err.Error()) }
	o := f.create(t)
	f.auth.revokedUser = f.p.UserID
	o = f.reconcile(t, o)
	if o.Status != "failed" || len(logged) != 1 || !strings.Contains(logged[0], o.ID) {
		t.Fatalf("status %s, logged %v", o.Status, logged)
	}
}

// A managed server whose create failed before a provider resource ID was
// recorded (and was optionally drained) can still be previewed for deletion,
// accepted, and cleaned up, releasing its capacity reservation.
func TestCloudServerFailedCreateWithoutResourceIDCanBeDeleted(t *testing.T) {
	for _, drain := range []bool{false, true} {
		name := "no drain"
		if drain {
			name = "drained"
		}
		t.Run(name, func(t *testing.T) {
			f := ledgerFixture(t)
			f.provider.lost = true
			o, err := f.svc.PreviewServer(t.Context(), f.p, "cloud", cloudinfra.ServerInput{
				Kind:          "create",
				Name:          "Digital Ocean Droplet 1 2VPC 4GB",
				ConnectionID:  "infra-account",
				CloudRevision: 1,
				Region:        "region",
				Size:          "small",
				Image:         "ubuntu",
			})
			if err != nil {
				t.Fatalf("preview create: %v", err)
			}
			o, err = f.svc.ApplyServerOperation(t.Context(), f.p, "cloud", o.ID, cloudinfra.ApplyInput{Review: o.Review})
			if err != nil {
				t.Fatalf("apply create: %v", err)
			}
			// Simulate provider rejecting creation before any droplet exists.
			f.provider.beforeCreate = func(provider.ServerSpec) {
				f.provider.remote = provider.InfrastructureServer{}
			}
			o = f.reconcile(t, o)
			// Ensure remote stays empty (no droplet was created).
			f.provider.remote = provider.InfrastructureServer{}
			if o.Status != "failed" || o.ResourceID != "" {
				t.Fatalf("after failed create: %+v", o)
			}
			server, err := f.store.GetServer(t.Context(), "org", "cloud", o.ServerID)
			if err != nil {
				t.Fatal(err)
			}
			if server.ResourceID != "" {
				t.Fatalf("expected empty server resource_id, got %q", server.ResourceID)
			}
			if drain {
				review, derr := f.svc.PreviewDrain(t.Context(), f.p, "cloud", server.ID)
				if derr != nil {
					t.Fatalf("preview drain: %v", derr)
				}
				if server, derr = f.svc.ApplyDrain(t.Context(), f.p, "cloud", server.ID, cloudinfra.ApplyInput{Review: review.Review}); derr != nil {
					t.Fatalf("apply drain: %v", derr)
				}
			}
			preview, err := f.svc.PreviewServer(t.Context(), f.p, "cloud", cloudinfra.ServerInput{
				Kind:           "delete",
				ServerID:       server.ID,
				CloudRevision:  1,
				ServerRevision: server.Revision,
				ConnectionID:   "infra-account",
				Name:           server.Name,
				Region:         server.Region,
				Size:           server.Size,
			})
			if err != nil {
				t.Fatalf("preview delete: %v", err)
			}
			if _, err = f.svc.ApplyServerOperation(t.Context(), f.p, "cloud", preview.ID, cloudinfra.ApplyInput{Review: preview.Review}); err != nil {
				t.Fatalf("apply delete: %v", err)
			}
			preview = f.reconcile(t, preview)
			if preview.Status != "succeeded" {
				t.Fatalf("delete: %+v", preview)
			}
			deleted, err := f.store.GetServer(t.Context(), "org", "cloud", server.ID)
			if err != nil || deleted.Status != "deleted" {
				t.Fatalf("server after delete: %+v %v", deleted, err)
			}
			created, err := f.store.OperationByID(t.Context(), "org", o.ID)
			if err != nil || created.Status != "cancelled" || !strings.Contains(created.Message, "deletion was accepted") {
				t.Fatalf("the failed create: %+v %v", created, err)
			}
			// Capacity reservation was released and inventory remains fresh.
			next := ledgerPreview(t, f)
			if err = f.store.Accept(t.Context(), next); err != nil {
				t.Fatalf("accepting replacement create after deleting failed server: %v", err)
			}
		})
	}
}

// A create whose server never enrolls before its bootstrap expires fails
// with the reason, and so does its server, rather than starting forever.
// Health can't be checked while it starts or once it failed, and says so.
func TestCloudServerThatNeverEnrollsFailsWithTheReason(t *testing.T) {
	f := newInfraFixture(t)
	f.svc.ManagedSSHPort = 2222
	o := f.reconcile(t, f.create(t))
	if o.Status != "waiting" || o.SSHPort != 2222 {
		t.Fatalf("after create: %+v", o)
	}
	_, err := f.svc.CheckServer(t.Context(), f.p, "cloud", o.ServerID)
	if !errors.Is(err, cloudinfra.ErrConflict) || !strings.Contains(err.Error(), "still starting") {
		t.Fatalf("checking a starting server: %v", err)
	}

	o.BootstrapExpiresAt = time.Now().Add(-time.Minute)
	if err = f.store.SaveOperation(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	o = f.reconcile(t, o)
	if o.Status != "failed" || !strings.Contains(o.Message, "SSH on port 2222") {
		t.Fatalf("expired enrollment: %+v", o)
	}
	server, err := f.store.GetServer(t.Context(), "org", "cloud", o.ServerID)
	if err != nil || server.Status != "failed" || server.Message != o.Message {
		t.Fatalf("server after its create failed: %+v %v", server, err)
	}
	_, err = f.svc.CheckServer(t.Context(), f.p, "cloud", o.ServerID)
	if !errors.Is(err, cloudinfra.ErrConflict) || !strings.Contains(err.Error(), "failed to start") {
		t.Fatalf("checking a failed server: %v", err)
	}

	// The failed server can still be deleted.
	f.provider.remote.ID = ""
	preview, err := f.svc.PreviewServer(t.Context(), f.p, "cloud", cloudinfra.ServerInput{Kind: "delete", ServerID: server.ID, CloudRevision: 1, ServerRevision: server.Revision})
	if err != nil {
		t.Fatalf("preview delete: %v", err)
	}
	if _, err = f.svc.ApplyServerOperation(t.Context(), f.p, "cloud", preview.ID, cloudinfra.ApplyInput{Review: preview.Review}); err != nil {
		t.Fatalf("apply delete: %v", err)
	}
	if preview = f.reconcile(t, preview); preview.Status != "succeeded" {
		t.Fatalf("delete: %+v", preview)
	}
}

// With Loomplane's addresses set, a create makes the SSH firewall before the
// server, puts the server in it, and keeps Loomplane's user off port 22.
// A health check someone asks for brings the firewall up to date with
// Loomplane's addresses, and an expired enrollment names the setting.
func TestCloudServerJoinsTheSSHFirewall(t *testing.T) {
	f := newInfraFixture(t)
	f.svc.ManagedSSHPort = 2222
	f.svc.ManagedSSHAllowedCIDRs = []string{"203.0.113.7/32"}
	var spec provider.ServerSpec
	f.provider.beforeCreate = func(s provider.ServerSpec) {
		spec = s
		if len(f.provider.firewalls) == 0 {
			t.Error("the server was created before its firewall")
		}
	}
	o := f.reconcile(t, f.create(t))
	if o.Status != "waiting" || !o.SSHFirewall || o.SSHPort != 2222 {
		t.Fatalf("after create: %+v", o)
	}
	if spec.FirewallTag != "loomplane-ssh-2222" || !strings.Contains(spec.UserData, "Match User loomplane LocalPort 22") {
		t.Fatalf("spec: tag %q, user data keeps loomplane off 22: %v", spec.FirewallTag, strings.Contains(spec.UserData, "Match User loomplane"))
	}
	if fw := f.provider.firewalls[0]; fw.SSHPort != 2222 || len(fw.Sources) != 1 || fw.Sources[0] != "203.0.113.7/32" {
		t.Fatalf("firewall: %+v", fw)
	}

	f.svc.ManagedSSHAllowedCIDRs = []string{"192.0.2.10/32"}
	if _, err := f.svc.CheckServer(t.Context(), f.p, "cloud", o.ServerID); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("checking a starting server: %v", err)
	}
	if fw := f.provider.firewalls[len(f.provider.firewalls)-1]; len(fw.Sources) != 1 || fw.Sources[0] != "192.0.2.10/32" {
		t.Fatalf("firewall after Loomplane's addresses changed: %+v", fw)
	}

	o.BootstrapExpiresAt = time.Now().Add(-time.Minute)
	if err := f.store.SaveOperation(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	if o = f.reconcile(t, o); o.Status != "failed" || !strings.Contains(o.Message, "LOOMPLANE_MANAGED_SSH_ALLOWED_CIDRS") || !o.SSHFirewall {
		t.Fatalf("expired enrollment: %+v", o)
	}
}

// Without Loomplane's addresses nothing changes: no firewall, no tag.
func TestCloudServerWithoutAllowedCIDRsHasNoFirewall(t *testing.T) {
	f := newInfraFixture(t)
	f.svc.ManagedSSHPort = 2222
	var spec provider.ServerSpec
	f.provider.beforeCreate = func(s provider.ServerSpec) { spec = s }
	o := f.reconcile(t, f.create(t))
	if o.Status != "waiting" || o.SSHFirewall || spec.FirewallTag != "" || len(f.provider.firewalls) != 0 || strings.Contains(spec.UserData, "Match User") {
		t.Fatalf("create: %+v, tag %q, firewalls %v", o, spec.FirewallTag, f.provider.firewalls)
	}
}

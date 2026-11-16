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

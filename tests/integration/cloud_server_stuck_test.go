//go:build integration

package integration

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
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

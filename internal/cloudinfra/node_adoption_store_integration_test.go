//go:build integration

package cloudinfra

import (
	"errors"
	"testing"
	"time"
)

func TestNodeAdoptionStoreKeepsTheApproval(t *testing.T) {
	pg := routeBindingDatabase(t)
	ctx := t.Context()
	if _, err := pg.Exec(ctx, `INSERT INTO loomplane_nodes (id,org_id,name,public_key,key_fingerprint,created_at,updated_at) VALUES ('node_1','org','web-1','\x01','fp',now(),now())`); err != nil {
		t.Fatal(err)
	}
	st := NewPgStore(pg)
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := &NodeAdoption{NodeID: "node_1", OrgID: "org", CloudID: "cloud", ApprovedBy: "user_admin", ApprovedByKeyID: "key_admin", BuildWorkers: "dedicated", Status: NodeAdoptionWaiting, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateNodeAdoption(ctx, a); err != nil {
		t.Fatal(err)
	}
	// A second record leaves the first alone.
	again := *a
	again.CloudID, again.ApprovedBy = "cloud_2", "user_other"
	if err := st.CreateNodeAdoption(ctx, &again); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetNodeAdoption(ctx, "org", "node_1")
	if err != nil {
		t.Fatal(err)
	}
	if *got != *a {
		t.Fatalf("read %+v, want %+v", got, a)
	}
	if _, err = st.GetNodeAdoption(ctx, "other", "node_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org read it: %v", err)
	}
	// Saving moves the status and its outcome, never who approved it.
	got.Status, got.Reason, got.ServerID, got.OperationID, got.UpdatedAt = NodeAdoptionApplied, "", "server_1", "infraop_1", now.Add(time.Minute)
	got.ApprovedBy, got.CloudID = "user_other", "cloud_2"
	if err = st.SaveNodeAdoption(ctx, got); err != nil {
		t.Fatal(err)
	}
	saved, err := st.GetNodeAdoption(ctx, "org", "node_1")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != NodeAdoptionApplied || saved.ServerID != "server_1" || saved.OperationID != "infraop_1" || saved.ApprovedBy != "user_admin" || saved.CloudID != "cloud" {
		t.Fatalf("saved %+v", saved)
	}
	if err = st.SaveNodeAdoption(ctx, &NodeAdoption{NodeID: "node_1", OrgID: "other", Status: NodeAdoptionFailed}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org saved it: %v", err)
	}
	// A Cloud's list carries node names, and nothing of another Cloud's.
	if _, err = pg.Exec(ctx, `INSERT INTO loomplane_nodes (id,org_id,name,public_key,key_fingerprint,created_at,updated_at) VALUES ('node_2','org','web-2','\x02','fp2',now(),now())`); err != nil {
		t.Fatal(err)
	}
	if err = st.CreateNodeAdoption(ctx, &NodeAdoption{NodeID: "node_2", OrgID: "org", Status: NodeAdoptionFailed, Reason: "refused", ApprovedBy: "user_admin", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListNodeAdoptions(ctx, "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].NodeID != "node_1" || list[0].NodeName != "web-1" || list[0].Status != NodeAdoptionApplied || list[0].BuildWorkers != "dedicated" {
		t.Fatalf("list %+v", list)
	}
	if other, listErr := st.ListNodeAdoptions(ctx, "other", "cloud"); listErr != nil || len(other) != 0 {
		t.Fatalf("another org listed %+v %v", other, listErr)
	}
}

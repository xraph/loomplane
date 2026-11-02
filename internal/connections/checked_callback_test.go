package connections

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/security"
)

type checkedPersistence struct {
	*memStore
	createErr, updateErr error
}

func (s *checkedPersistence) CreateConnection(ctx context.Context, c *Connection) error {
	if s.createErr != nil {
		return s.createErr
	}
	return s.memStore.CreateConnection(ctx, c)
}
func (s *checkedPersistence) UpdateConnection(ctx context.Context, c *Connection) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	return s.memStore.UpdateConnection(ctx, c)
}
func TestCheckedCallbackOnlySeesPersistedReadySnapshots(t *testing.T) {
	store := &checkedPersistence{memStore: &memStore{rows: map[string]Connection{}}}
	audit := &connAudit{}
	svc := NewService(store, &memSecrets{kept: map[string][]byte{}}, map[string]Checker{DigitalOcean: identityChecker{result: Check{Account: "Acme", Details: map[string]string{"account_uuid": "account"}}}}, identityAuthz{}, audit, nil)
	p := security.Principal{OrgID: "org", UserID: "user"}
	var received []Connection
	svc.Checked = func(_ context.Context, actor security.Principal, c Connection) {
		if actor != p {
			t.Errorf("actor changed: %+v", actor)
		}
		if _, err := store.GetConnection(t.Context(), p.OrgID, c.ID); err != nil {
			t.Errorf("callback before persistence: %v", err)
		}
		received = append(received, c)
		c.Details["account_uuid"] = "callback mutation"
		if c.CheckedAt != nil {
			*c.CheckedAt = c.CheckedAt.AddDate(5, 0, 0)
		}
	}
	c, err := svc.Create(t.Context(), p, CreateInput{Cloud: DigitalOcean, Name: "account", Credential: "TOPSECRET"})
	if err != nil {
		t.Fatal(err)
	}
	if len(received) != 1 || c.Details["account_uuid"] != "account" || c.CheckedAt.Year() > 2030 {
		t.Fatalf("mutable callback snapshot: %+v", c)
	}
	if _, err = svc.Recheck(t.Context(), p, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Replace(t.Context(), p, c.ID, ReplaceInput{Credential: "ROTATEDSECRET"}); err != nil {
		t.Fatal(err)
	}
	if len(received) != 3 || len(audit.actions) != 3 {
		t.Fatalf("callbacks=%d audit=%v", len(received), audit.actions)
	}
	store.updateErr = errors.New("persistence unavailable")
	if _, err = svc.Recheck(t.Context(), p, c.ID); err == nil {
		t.Fatal("expected update failure")
	}
	if len(received) != 3 {
		t.Fatal("failed save invoked callback")
	}
	store.createErr = errors.New("persistence unavailable")
	if _, err = svc.Create(t.Context(), p, CreateInput{Cloud: DigitalOcean, Name: "another", Credential: "SECRET"}); err == nil {
		t.Fatal("expected insert failure")
	}
	if len(received) != 3 {
		t.Fatal("failed insert invoked callback")
	}
}
func TestCheckedCallbackSkipsIncompleteAndUnrelatedEvents(t *testing.T) {
	store := &memStore{rows: map[string]Connection{}}
	svc := NewService(store, &memSecrets{kept: map[string][]byte{}}, map[string]Checker{DigitalOcean: identityChecker{result: Check{Account: "Acme", Details: map[string]string{"account_uuid": "account"}, Missing: []string{"database:read"}}}}, identityAuthz{}, nil, nil)
	calls := 0
	svc.Checked = func(context.Context, security.Principal, Connection) { calls++ }
	p := security.Principal{OrgID: "org", UserID: "user"}
	c, err := svc.Create(t.Context(), p, CreateInput{Cloud: DigitalOcean, Name: "incomplete", Credential: "SECRET"})
	if err != nil || c.Status != StatusIncomplete {
		t.Fatalf("incomplete: %+v %v", c, err)
	}
	if calls != 0 {
		t.Fatal("incomplete account emitted successful callback")
	}
	c.Status = StatusReady
	svc.record(t.Context(), "cloud_connection.delete", p, c)
	if calls != 0 {
		t.Fatal("deletion emitted successful callback")
	}
}

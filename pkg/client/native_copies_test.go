package client

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeRecoveryWaitRetainsLatestWhenReadPermissionIsRevoked(t *testing.T) {
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer actor-key" || r.URL.Path != "/api/v1/native-recovery-copies/copy" {
			t.Error("wrong native observation", r.URL)
		}
		if reads.Add(1) > 1 {
			http.Error(w, `{"message":"permission revoked"}`, http.StatusForbidden)
			return
		}
		fmt.Fprint(w, `{"id":"copy","status":"running","expires_at":"2026-10-24T02:00:00Z","verified_at":null,"target":{"grant_revision":9007199254740993}}`)
	}))
	defer srv.Close()
	record, err := New(srv.URL, "actor-key").WaitNativeRecoveryCopy(t.Context(), "copy")
	if !errors.Is(err, ErrForbidden) || record == nil || record.ID != "copy" || record.Status != "running" || record.VerifiedAt != nil || record.Target.GrantRevision != 9007199254740993 || !record.ExpiresAt.Equal(time.Date(2026, 10, 24, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("lost retained copy on read denial: %+v %v", record, err)
	}
	if reads.Load() != 2 {
		t.Fatal("retried after permission denial")
	}
}

func TestNativeCopyClientRejectsUnacknowledgedAdmissionAndInvalidPageBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := New(srv.URL, "key")
	for _, in := range []NativeCopyRequest{{RequestID: "request", ReviewToken: "token"}, {AcknowledgeCharges: true, ReviewToken: "token"}, {AcknowledgeCharges: true, RequestID: "request"}} {
		if _, err := c.CreateNativeRecoveryCopy(t.Context(), in); err == nil {
			t.Fatal("accepted incomplete approval")
		}
	}
	if _, err := c.NativeRecoveryCopies(t.Context(), "project", NativeCopyPage{Limit: 101}); err == nil {
		t.Fatal("accepted invalid page")
	}
	if calls.Load() != 0 {
		t.Fatal("local validation contacted API")
	}
}

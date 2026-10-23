//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

type nativeAdmissionAPI interface {
	ReviewCopy(context.Context, security.Principal, string, string, recovery.NativeCopyReviewInput) (*recovery.NativeCopyReview, error)
	CreateCopy(context.Context, security.Principal, recovery.NativeCopyCreateInput) (*recovery.NativeRestore, error)
	GetCopy(context.Context, security.Principal, string) (*recovery.NativeRestore, error)
	ListCopies(context.Context, security.Principal, string, recovery.NativePageInput) (*recovery.NativeCopiesPage, error)
}

type nativeReviewAuth struct{}

func (nativeReviewAuth) Require(_ context.Context, p security.Principal, permission security.Permission, project string) error {
	if p.OrgID != "org" || p.UserID != "operator" || project != "project" || permission != security.RestoreManage && permission != security.BackupRead {
		return security.ErrForbidden
	}
	return nil
}

func resignNativeReview(t *testing.T, token string, key []byte, field string, value any) string {
	t.Helper()
	payload, _, ok := strings.Cut(token, ".")
	if !ok {
		t.Fatal("review has no signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err = json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	claims[field] = value
	raw, err = json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	payload = base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestNativeCopyReviewBoundsAdmissionAndReplaysWithoutAnotherProviderCall(t *testing.T) {
	f, admission, request, _ := nativeAdmissionFixture(t)
	selection := &nativeAdmissionSelection{}
	key := bytes.Repeat([]byte{41}, 32)
	queueFailure := errors.New("queue temporarily unavailable")
	enqueues := 0
	s := &recovery.NativeService{Store: f.store, Copies: f.store, Cleanups: f.store, Admission: admission, ReviewKey: key, Projects: projects.NewPgStore(f.db), Authz: nativeReviewAuth{}, Backend: selection, EnqueueCopy: func(context.Context, string, string) error { enqueues++; return queueFailure }}
	api, ok := any(s).(nativeAdmissionAPI)
	if !ok {
		t.Fatal("native copies have no reviewed public admission and scoped read service")
	}
	p := security.Principal{OrgID: "org", UserID: "operator"}
	review, err := api.ReviewCopy(t.Context(), p, request.SourceID, request.PointID, recovery.NativeCopyReviewInput{RequestID: "review-api-request", CloudID: "native-cloud", TargetID: "native-target", TTLHours: 1})
	if err != nil || review.Capacity.ComputeClass != "db-custom-1-3840" || review.Capacity.StorageGB != 10 || review.TTLHours != 1 || review.ValidUntil.Before(time.Now()) || review.ValidUntil.After(time.Now().Add(11*time.Minute)) || review.Notice == "" {
		t.Fatal("copy review omitted bounded capacity, lifetime or cost notice", review, err)
	}
	input := recovery.NativeCopyCreateInput{RequestID: "review-api-request", ReviewToken: review.ReviewToken, AcknowledgeCharges: true}
	for _, scenario := range []string{"no-charge-ack", "bad-request", "tamper", "wrong-actor", "wrong-org", "expired"} {
		in, actor := input, p
		switch scenario {
		case "no-charge-ack":
			in.AcknowledgeCharges = false
		case "bad-request":
			in.RequestID = "short"
		case "tamper":
			in.ReviewToken = "x" + in.ReviewToken
		case "wrong-actor":
			actor.UserID = "other-user"
		case "wrong-org":
			actor.OrgID = "foreign"
		case "expired":
			in.ReviewToken = resignNativeReview(t, input.ReviewToken, key, "valid_until", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano))
		}
		if _, createErr := api.CreateCopy(t.Context(), actor, in); createErr == nil {
			t.Fatal("unsafe admission accepted", scenario)
		}
	}
	if enqueues != 0 || selection.calls != 1 {
		t.Fatal("rejected admission enqueued work or reread native provider", enqueues, selection.calls)
	}
	r, err := api.CreateCopy(t.Context(), p, input)
	if !errors.Is(err, queueFailure) || r == nil || r.Status != "queued" || r.ExpiresAt.Sub(r.CreatedAt) != time.Hour || selection.calls != 2 || enqueues != 1 {
		t.Fatal("enqueue gap lost admitted copy or its reviewed lifetime", r, err, selection.calls, enqueues)
	}
	extra := input
	extra.RequestID = "another-copy-request"
	if _, err = api.CreateCopy(t.Context(), p, extra); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("one reviewed copy approval admitted another request", err)
	}
	// Replays stay useful after the review expires or native point disappears.
	// They repair enqueue only; they cannot extend the admitted lifetime.
	input.ReviewToken = resignNativeReview(t, input.ReviewToken, key, "valid_until", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano))
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_native_points SET present=false,body=jsonb_set(body,'{status}','"DELETED"') WHERE id=$1`, request.PointID); err != nil {
		t.Fatal(err)
	}
	replay, err := api.CreateCopy(t.Context(), p, input)
	if !errors.Is(err, queueFailure) || replay == nil || replay.ID != r.ID || !replay.ExpiresAt.Equal(r.ExpiresAt) || selection.calls != 2 || enqueues != 2 {
		t.Fatal("replay depended on expired review or provider point", replay, err, selection.calls, enqueues)
	}
	input.ReviewToken = resignNativeReview(t, input.ReviewToken, key, "ttl_hours", 2)
	if _, err = api.CreateCopy(t.Context(), p, input); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("replay changed admitted lifetime", err)
	}
	got, err := api.GetCopy(t.Context(), p, r.ID)
	if err != nil || got.ID != r.ID {
		t.Fatal("copy status is not readable", got, err)
	}
	page, err := api.ListCopies(t.Context(), p, "project", recovery.NativePageInput{EnvironmentID: "environment", Limit: 1})
	if err != nil || len(page.Copies) != 1 || page.Copies[0].ID != r.ID {
		t.Fatal("copy not in scoped environment list", page, err)
	}
	if _, err = api.GetCopy(t.Context(), security.Principal{OrgID: "foreign", UserID: "operator"}, r.ID); err == nil {
		t.Fatal("copy status escaped organization scope")
	}
}

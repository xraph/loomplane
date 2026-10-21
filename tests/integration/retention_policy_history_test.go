//go:build integration

package integration

import (
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/recovery"
)

func TestRetentionPolicyHistoryPaginatesCommittedRevisionsWithoutCrossingScope(t *testing.T) {
	_, store := retirementFixture(t)
	input := retentionPolicyRequest()
	input.Enabled = false
	policy, err := store.PutRetentionPolicy(t.Context(), input, 0, retentionGuard)
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		input.Policy.OlderThanDays++
		policy, err = store.PutRetentionPolicy(t.Context(), input, policy.Revision, retentionGuard)
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.RetentionPolicyHistory(t.Context(), "o", "p", policy.ID, recovery.RetentionPolicyHistoryInput{Limit: 2})
	if err != nil || len(page.Events) != 2 || page.Events[0].Revision != 6 || page.Events[1].Revision != 5 || page.NextBefore != "5" {
		t.Fatalf("first revision page: %+v %v", page, err)
	}
	policy, err = store.PutRetentionPolicy(t.Context(), input, policy.Revision, retentionGuard)
	if err != nil {
		t.Fatal(err)
	}
	page, err = store.RetentionPolicyHistory(t.Context(), "o", "p", policy.ID, recovery.RetentionPolicyHistoryInput{Limit: 2, Before: page.NextBefore})
	if err != nil || len(page.Events) != 2 || page.Events[0].Revision != 4 || page.Events[1].Revision != 3 || page.NextBefore != "3" {
		t.Fatalf("stable continuation: %+v %v", page, err)
	}
	for _, scope := range [][2]string{{"foreign", "p"}, {"o", "foreign"}} {
		if _, err = store.RetentionPolicyHistory(t.Context(), scope[0], scope[1], policy.ID, recovery.RetentionPolicyHistoryInput{}); !errors.Is(err, recovery.ErrNotFound) {
			t.Fatalf("history crossed scope: %v", err)
		}
	}
	for _, in := range []recovery.RetentionPolicyHistoryInput{{Limit: 101}, {Limit: -1}, {Before: "01"}, {Before: "-1"}, {Before: "0"}, {Before: "9223372036854775808"}} {
		if _, err = store.RetentionPolicyHistory(t.Context(), "o", "p", policy.ID, in); !errors.Is(err, recovery.ErrInvalid) {
			t.Fatalf("invalid history query: %+v %v", in, err)
		}
	}
}

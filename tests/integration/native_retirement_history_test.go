//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/recovery"
)

type nativeRetirementHistories interface {
	NativeRetirementHistory(context.Context, string, string, recovery.RetirementHistoryInput) (*recovery.RetirementHistory, error)
}

func TestNativeRetirementHistoryKeepsTransitionsAndScopedCursors(t *testing.T) {
	f, store, request := nativeRetirementFixture(t)
	histories, ok := any(f.store).(nativeRetirementHistories)
	if !ok {
		t.Fatal("native retirement has no committed history reader")
	}
	guard := func(context.Context) error { return nil }
	r, err := store.AdmitNativeRetirement(t.Context(), request, guard)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimNativeRetirement(t.Context(), "org", r.ID, "history-worker-one")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveNativeRetirementIntent(t.Context(), claimed, true); err != nil {
		t.Fatal(err)
	}
	if err = store.FinishNativeRetirement(t.Context(), claimed, recovery.ErrNativeUnavailable); err != nil {
		t.Fatal(err)
	}
	if _, err = store.RetryNativeRetirement(t.Context(), "org", r.ID, "operator", guard); err != nil {
		t.Fatal(err)
	}
	next, err := store.ClaimNativeRetirement(t.Context(), "org", r.ID, "history-worker-two")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishNativeRetirement(t.Context(), next, nil); err != nil {
		t.Fatal(err)
	}
	first, err := histories.NativeRetirementHistory(t.Context(), "org", r.ID, recovery.RetirementHistoryInput{Limit: 2})
	if err != nil || len(first.Events) != 2 || first.NextBefore == "" {
		t.Fatal("native retirement history lost bounded cursor", first, err)
	}
	second, err := histories.NativeRetirementHistory(t.Context(), "org", r.ID, recovery.RetirementHistoryInput{Before: first.NextBefore, Limit: 100})
	if err != nil || len(second.Events) != 5 || second.NextBefore != "" {
		t.Fatal("native retirement history lost committed transitions", second, err)
	}
	if first.Events[0].Kind != "finished" || first.Events[0].Status != "succeeded" || second.Events[4].Kind != "admitted" {
		t.Fatal("native retirement history order is incorrect")
	}
	if _, err = histories.NativeRetirementHistory(t.Context(), "other-org", r.ID, recovery.RetirementHistoryInput{}); !errors.Is(err, recovery.ErrNotFound) {
		t.Fatal("native history crossed organization", err)
	}
	if _, err = histories.NativeRetirementHistory(t.Context(), "org", r.ID, recovery.RetirementHistoryInput{Before: "999999999999"}); !errors.Is(err, recovery.ErrNotFound) {
		t.Fatal("native history accepted a foreign cursor", err)
	}
}

func TestNativeRetirementListUsesSourceScopedCursor(t *testing.T) {
	f, _, request := nativeRetirementFixture(t)
	lister, ok := any(f.store).(interface {
		ListNativeRetirements(context.Context, string, string, recovery.NativePageInput) (*recovery.NativeRetirementPage, error)
	})
	if !ok {
		t.Fatal("native deletions cannot be found after reload")
	}
	if _, err := f.store.AdmitNativeRetirement(t.Context(), request, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	page, err := lister.ListNativeRetirements(t.Context(), "org", f.source.ID, recovery.NativePageInput{Limit: 1})
	if err != nil || len(page.Retirements) != 1 || page.Retirements[0].ID != request.ID || page.NextBefore != "" {
		t.Fatal("native retirement list lost source scope", page, err)
	}
	if _, err = lister.ListNativeRetirements(t.Context(), "other-org", f.source.ID, recovery.NativePageInput{}); !errors.Is(err, recovery.ErrNotFound) {
		t.Fatal("native retirement list crossed organization", err)
	}
	if _, err = lister.ListNativeRetirements(t.Context(), "org", f.source.ID, recovery.NativePageInput{Before: "foreign"}); !errors.Is(err, recovery.ErrNotFound) {
		t.Fatal("native retirement list accepted foreign cursor", err)
	}
}

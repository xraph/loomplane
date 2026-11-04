package metering

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/xraph/forge"
)

func TestNoopsAllowAndDiscard(t *testing.T) {
	ctx := t.Context()
	for _, k := range []Kind{KindDeploy, KindBuild, KindAIPlatform} {
		if err := AllowAll.CanRun(ctx, "org", k, true); err != nil {
			t.Fatalf("CanRun %s: %v", k, err)
		}
	}
	e := Event{ID: "e1", OrgID: "org", Feature: "vm", Quantity: 1, OccurredAt: time.Now()}
	for range 2 {
		if err := Discard.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBlockedMatchesErrBlockedAndReadsAsItsReason(t *testing.T) {
	var err error = &Blocked{Reason: "Add a card in Settings > Billing to start running anything on Loomplane Cloud."}
	wrapped := fmt.Errorf("Loomplane Cloud can't start this for the workspace: %w", err)
	if !errors.Is(wrapped, ErrBlocked) {
		t.Fatal("a wrapped Blocked doesn't match ErrBlocked")
	}
	if err.Error() != "Add a card in Settings > Billing to start running anything on Loomplane Cloud." {
		t.Fatalf("Error() = %q", err.Error())
	}
}

type refuse struct{ calls int }

func (r *refuse) CanRun(context.Context, string, Kind, bool) error {
	r.calls++
	return &Blocked{Reason: "no"}
}

type count struct{ n int }

func (c *count) Record(context.Context, Event) error { c.n++; return nil }

func TestLateAllowsUntilSetThenDelegates(t *testing.T) {
	var l Late
	if err := l.CanRun(t.Context(), "org", KindDeploy, false); err != nil {
		t.Fatalf("unset Late refused: %v", err)
	}
	if err := l.Record(t.Context(), Event{ID: "e"}); err != nil {
		t.Fatal(err)
	}
	g, m := &refuse{}, &count{}
	l.Set(g, m)
	if err := l.CanRun(t.Context(), "org", KindDeploy, false); !errors.Is(err, ErrBlocked) {
		t.Fatalf("set Late didn't delegate: %v", err)
	}
	_ = l.Record(t.Context(), Event{ID: "e"})
	if g.calls != 1 || m.n != 1 {
		t.Fatalf("calls %d records %d", g.calls, m.n)
	}
}

func TestResolveFallsBackToAllowAllAndDiscard(t *testing.T) {
	g, m, err := Resolve(forge.NewContainer())
	if err != nil || g != AllowAll || m != Discard {
		t.Fatalf("Resolve on an empty container = %v %v %v", g, m, err)
	}
	c := forge.NewContainer()
	want := &refuse{}
	if err = forge.ProvideValue[Gate](c, want); err != nil {
		t.Fatal(err)
	}
	if g, _, _ = Resolve(c); g != Gate(want) {
		t.Fatalf("Resolve ignored the provided gate: %v", g)
	}
}

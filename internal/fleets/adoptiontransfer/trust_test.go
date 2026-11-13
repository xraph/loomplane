package adoptiontransfer

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type proofReaderFunc func(context.Context, Intent, string) (Receipt, error)

func (f proofReaderFunc) ReadTransferReceipt(ctx context.Context, i Intent, ref string) (Receipt, error) {
	return f(ctx, i, ref)
}
func TestTrustedReceiptReloadsExactPendingIdentity(t *testing.T) {
	_, s, now := fixture(t)
	s = claim(t, s, "worker", now)
	s = apply(t, s, Command{Kind: Begin, Step: Fence, AttemptID: "original"}, now)
	expected := receipt(t, s, Fence, Confirmed, now)
	called := false
	reader := proofReaderFunc(func(_ context.Context, i Intent, _ string) (Receipt, error) {
		called = true
		if i.Digest() != s.Intent().Digest() {
			t.Fatal("lost trusted lookup pins")
		}
		return expected, nil
	})
	r, err := ResolveReceipt(context.Background(), reader, s, "fence-confirmed", now)
	if err != nil || !called || r.Digest() != expected.Digest() {
		t.Fatalf("lookup: %v", err)
	}
	if _, err = ResolveReceipt(context.Background(), nil, s, "fence-confirmed", now); !errors.Is(err, ErrQualificationRequired) {
		t.Fatal(err)
	}
	if _, err = ResolveReceipt(context.Background(), reader, s, "another-reference", now); !errors.Is(err, ErrReceipt) {
		t.Fatal("reference does not identify returned receipt", err)
	}
	for _, change := range []func(*ReceiptFields){
		func(f *ReceiptFields) { f.IntentDigest = strings.Repeat("2", 64) },
		func(f *ReceiptFields) { f.AttemptID = "other-attempt" },
		func(f *ReceiptFields) { f.ObservedAt = now.AddDate(0, 0, 1) },
	} {
		f := expected.Fields()
		change(&f)
		wrong, _ := NewReceipt(f)
		reader = func(context.Context, Intent, string) (Receipt, error) { return wrong, nil }
		if _, err = ResolveReceipt(context.Background(), reader, s, "fence-confirmed", now); !errors.Is(err, ErrReceipt) {
			t.Fatal("accepted unrelated proof", err)
		}
	}
}

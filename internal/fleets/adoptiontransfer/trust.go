package adoptiontransfer

import (
	"context"
	"time"
)

// ResolveReceipt reloads a proof from its trusted owner and binds it to the
// accepted intent and original attempt. It grants no execution authority.
func ResolveReceipt(ctx context.Context, reader ReceiptReader, state State, reference string, now time.Time) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if reader == nil {
		return Receipt{}, ErrQualificationRequired
	}
	if !validID(reference) {
		return Receipt{}, ErrInvalid
	}
	receipt, err := reader.ReadTransferReceipt(ctx, state.Intent(), reference)
	if err != nil {
		return Receipt{}, err
	}
	if receipt.Fields().ID != reference {
		return Receipt{}, ErrReceipt
	}
	for _, saved := range state.Receipts() {
		if saved.Fields().ID == reference {
			if saved == receipt {
				return saved, nil
			}
			return Receipt{}, ErrConflict
		}
	}
	if err := checkReceipt(state, receipt, canonicalTime(now)); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

package platformcloud

import "testing"

func TestSuspendMessage(t *testing.T) {
	cases := []struct {
		reasons []SuspendReason
		want    string
	}{
		{[]SuspendReason{ReasonSpendingLimit}, "Loomplane Cloud work is stopped because the spending limit was reached. It starts again when it is resolved."},
		{[]SuspendReason{ReasonPastDue}, "Loomplane Cloud work is stopped because a payment is past due. It starts again when it is resolved."},
		{[]SuspendReason{ReasonPlatformOwner}, "Loomplane Cloud work is stopped by the instance owner. It starts again when they restore it."},
		{[]SuspendReason{ReasonPastDue, ReasonSpendingLimit}, "Loomplane Cloud work is stopped because a payment is past due and the spending limit was reached. It starts again when both are resolved."},
		{[]SuspendReason{ReasonPastDue, ReasonSpendingLimit, ReasonPlatformOwner}, "Loomplane Cloud work is stopped because a payment is past due, the spending limit was reached and the instance owner stopped it. It starts again when all are resolved."},
	}
	for _, c := range cases {
		if got := suspendMessage(c.reasons); got != c.want {
			t.Errorf("suspendMessage(%v) = %q, want %q", c.reasons, got, c.want)
		}
	}
}

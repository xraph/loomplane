package notifications

import "testing"

// A crossed notice says what the model cost so far.
func TestModelBudgetCrossedCopy(t *testing.T) {
	title, body := modelBudgetCopy(modelBudgetPayload{Resource: "llama", Kind: ModelBudgetCrossed, BudgetUSD: "500.00", MonthToDateUSD: "512.34"})
	if title != "llama passed its $500.00 budget" ||
		body != "It has cost $512.34 so far this month. Loomplane won't stop it. Open the model to change where it runs or remove it." {
		t.Errorf("crossed copy: %q %q", title, body)
	}
}

// A notice that isn't one of the two kinds, or lacks its model, is refused
// before it reaches the outbox.
func TestModelBudgetNoticeIsChecked(t *testing.T) {
	s := &Service{}
	for _, n := range []ModelBudgetNotice{
		{OrgID: "o", ProjectID: "p", EnvironmentID: "e", DeploymentID: "d", Resource: "r", Month: "2026-11", Kind: "soon", BudgetUSD: "1.00"},
		{OrgID: "o", ProjectID: "p", EnvironmentID: "e", Resource: "r", Month: "2026-11", Kind: ModelBudgetCrossed, BudgetUSD: "1.00"},
	} {
		if err := s.EnqueueModelBudgetNoticeIn(t.Context(), nil, n); err == nil {
			t.Errorf("%+v queued", n)
		}
	}
}

package deployments

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// recordInto has the harness's job record its frontend into got, as the
// ledger does: it fills an empty identity or replaces exactly the one
// named, and refuses anything else.
func recordInto(h *nativeHarness, got *[]string) {
	h.job.frontendResource = func(_ context.Context, external, replacing string) error {
		if n := len(*got); n > 0 && (*got)[n-1] != external && (replacing == "" || (*got)[n-1] != replacing) {
			return fmt.Errorf("the ledger names %s, not %s", (*got)[n-1], replacing)
		}
		*got = append(*got, external)
		return nil
	}
}

// A build that fails after Vercel made the project still names it in the
// frontend ledger, which is then the only record of what to remove.
func TestVercelFailedBuildRecordsItsProject(t *testing.T) {
	h := newNativeHarness(t, "production", "production", false)
	h.api.failBuild = true
	var got []string
	recordInto(h, &got)
	if _, err := h.exec.Apply(t.Context(), h.job); err == nil {
		t.Fatal("a failed build succeeded")
	}
	id := h.api.projects["web-production-abc12345"]
	if id == "" || len(got) != 1 || got[0] != id {
		t.Fatalf("recorded %v, want [%s]", got, id)
	}
}

// A native frontend is its environment of the shared project, recorded as
// the same ref its binding gets, never as the bare shared project.
func TestVercelNativeRecordsItsEnvironmentRef(t *testing.T) {
	h := newNativeHarness(t, "staging", "staging", true)
	var got []string
	recordInto(h, &got)
	out := h.apply(t)
	if len(got) != 1 || got[0] != out.ExternalID {
		t.Fatalf("recorded %v, want [%s]", got, out.ExternalID)
	}
}

// When the ledger can't name the project, the deploy stops there rather
// than run a frontend nothing could remove.
func TestVercelStopsWhenTheLedgerCantRecord(t *testing.T) {
	h := newNativeHarness(t, "production", "production", false)
	h.job.frontendResource = func(context.Context, string, string) error { return errors.New("ledger down") }
	if _, err := h.exec.Apply(t.Context(), h.job); err == nil {
		t.Fatal("deployed without recording the frontend")
	}
	if len(h.api.deploys) != 0 {
		t.Fatalf("deployed %d times without a record", len(h.api.deploys))
	}
}

// A native deploy that Vercel refuses after it named its environment falls
// back, and the fallback's project replaces that name in the ledger, so
// nothing stale releases the slot while the project runs.
func TestVercelFallbackReplacesTheNativeName(t *testing.T) {
	h := newNativeHarness(t, "staging", "staging", true)
	h.api.customVarStatus = http.StatusForbidden
	var got []string
	recordInto(h, &got)
	out := h.apply(t)
	if len(got) != 2 || got[1] != out.ExternalID || got[0] == got[1] {
		t.Fatalf("recorded %v, want the native ref then %s", got, out.ExternalID)
	}
}

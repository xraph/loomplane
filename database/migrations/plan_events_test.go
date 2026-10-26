package migrations

import (
	"strings"
	"testing"
)

func TestPlanEventFunctions_replaceOnlyTheTwoFunctions(t *testing.T) {
	for _, leftovers := range []bool{true, false} {
		got := planEventFunctions(leftovers)
		if len(got) != 2 {
			t.Fatalf("leftovers %v: %d statements", leftovers, len(got))
		}
		for _, stmt := range got {
			if !strings.HasPrefix(stmt, "CREATE OR REPLACE FUNCTION ") {
				t.Fatalf("leftovers %v: not a function: %.60s", leftovers, stmt)
			}
			if strings.Contains(stmt, "'leftovers','[]'::jsonb,'artifacts','[]'::jsonb") != leftovers {
				t.Fatalf("leftovers %v: %.80s", leftovers, stmt)
			}
		}
	}
}

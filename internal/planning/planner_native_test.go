package planning

import (
	"context"
	"testing"

	"github.com/xraph/loomplane/internal/providers"
)

// fallbackRecords are the environment's fallback records, by
// provider, connection and scope.
type fallbackRecords struct {
	fell  map[[3]string]bool
	asked [][5]string
}

func (f *fallbackRecords) FellBack(_ context.Context, orgID, environmentID, provider, connectionID, scope string) (bool, error) {
	f.asked = append(f.asked, [5]string{orgID, environmentID, provider, connectionID, scope})
	return f.fell[[3]string{provider, connectionID, scope}], nil
}

// stagingWithWeb plans acme's staging against what runs there, with web on
// the Vercel project ref, and the fallback records records holds.
func stagingWithWeb(t *testing.T, ref string, records *fallbackRecords) *Result {
	t.Helper()
	live := deployed(t, compute(t, acme, "staging"))
	for _, b := range live {
		if b.Name == "web" {
			b.ExternalID, b.ConnectionID = ref, "conn_vercel"
		}
	}
	moves, err := nativeMoves(t.Context(), records, "org", "env_staging", live)
	if err != nil {
		t.Fatal(err)
	}
	return Compute(Input{
		Environment: "staging", Graph: polyglot(t), Manifest: parse(t, acme),
		Providers: providers.Registry(), Bindings: live, Connections: connected,
		NativeMoves: moves,
	})
}

func TestPlannerMarksNativeMove(t *testing.T) {
	records := &fallbackRecords{}
	r := stagingWithWeb(t, "prj_old", records)
	web := action(t, r, "service:web")
	want := "Moves into web's shared Vercel project as its staging environment"
	if web.Op != Replace || !web.NativeMove || web.Reason != want {
		t.Fatalf("web %s native move %v: %q, want %q", web.Op, web.NativeMove, web.Reason, want)
	}
	if len(records.asked) != 1 || records.asked[0] != [5]string{"org", "env_staging", "vercel", "conn_vercel", "web"} {
		t.Fatalf("asked %v", records.asked)
	}
	for _, a := range r.Actions {
		if a.Name != "web" && a.NativeMove {
			t.Errorf("%s moves too", a.Name)
		}
	}

	t.Run("a fallback stays where it is", func(t *testing.T) {
		fell := &fallbackRecords{fell: map[[3]string]bool{{"vercel", "conn_vercel", "web"}: true}}
		web := action(t, stagingWithWeb(t, "prj_old", fell), "service:web")
		if web.Op == Replace || web.NativeMove {
			t.Fatalf("web %s native move %v: %s", web.Op, web.NativeMove, web.Reason)
		}
	})

	t.Run("without the records nothing moves", func(t *testing.T) {
		moves, err := nativeMoves(t.Context(), nil, "org", "env_staging", deployed(t, compute(t, acme, "staging")))
		if err != nil || len(moves) != 0 {
			t.Fatalf("moves %v, %v", moves, err)
		}
	})
}

func TestPlannerLeavesNativeRefAlone(t *testing.T) {
	records := &fallbackRecords{}
	web := action(t, stagingWithWeb(t, "prj_1#env_9", records), "service:web")
	if web.Op == Replace || web.NativeMove {
		t.Fatalf("web %s native move %v: %s", web.Op, web.NativeMove, web.Reason)
	}
	if len(records.asked) != 0 {
		t.Fatalf("asked about a native ref: %v", records.asked)
	}
}

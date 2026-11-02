package releasehealth

import (
	"strings"
	"testing"
)

func TestMatrixCoversEveryBuiltInProvider(t *testing.T) {
	want := []string{"cloud-run", "ecs-fargate", "azure-container-apps", "kubernetes", "server", "render", "vercel", "digitalocean"}
	got := Matrix()
	if len(got) != len(want) {
		t.Fatalf("matrix has %d rows, want %d", len(got), len(want))
	}
	seen := map[string]bool{}
	for i, row := range got {
		if row.Provider != want[i] {
			t.Errorf("row %d is %q, want %q", i, row.Provider, want[i])
		}
		if seen[row.Provider] {
			t.Errorf("duplicate provider %q", row.Provider)
		}
		seen[row.Provider] = true
	}
}

func TestMatrixReturnsACopy(t *testing.T) {
	m := Matrix()
	m[0].Provider = "changed"
	if Matrix()[0].Provider != "cloud-run" {
		t.Fatal("changing the returned slice changed the matrix")
	}
}

func TestOnlyNativelyWeightedTargetsCanary(t *testing.T) {
	weighting := map[string]string{
		"cloud-run":            WeightingRevisions,
		"ecs-fargate":          WeightingTargetGroups,
		"azure-container-apps": WeightingRevisions,
	}
	for _, row := range Matrix() {
		w, canary := weighting[row.Provider]
		if row.Canary != canary {
			t.Errorf("%s: Canary = %v, want %v", row.Provider, row.Canary, canary)
		}
		if row.Weighting != w {
			t.Errorf("%s: Weighting = %q, want %q", row.Provider, row.Weighting, w)
		}
		if !canary && row.Note == "" {
			t.Errorf("%s: a target without a canary needs a note", row.Provider)
		}
	}
}

func TestSignalsMatchTheReaders(t *testing.T) {
	want := map[string]Support{
		"cloud-run":            {Requests: true, Errors: true, Latency: "p95", PerRevision: true},
		"ecs-fargate":          {Requests: true, Errors: true, Latency: "p95", PerRevision: true},
		"azure-container-apps": {Requests: true, Errors: true, Latency: "average", PerRevision: true},
		"render":               {Requests: true, Errors: true, Latency: "p95"},
		"kubernetes":           {},
		"server":               {},
		"vercel":               {},
		"digitalocean":         {},
	}
	for _, row := range Matrix() {
		w := want[row.Provider]
		if row.Requests != w.Requests || row.Errors != w.Errors || row.Latency != w.Latency || row.PerRevision != w.PerRevision {
			t.Errorf("%s: signals = {%v %v %q %v}, want {%v %v %q %v}", row.Provider,
				row.Requests, row.Errors, row.Latency, row.PerRevision,
				w.Requests, w.Errors, w.Latency, w.PerRevision)
		}
	}
}

func TestUnknownProviderIsNoCanary(t *testing.T) {
	got := SupportFor("my-plugin")
	want := Support{Provider: "my-plugin", Note: "Provider plugins have no traffic weighting Loomplane can drive."}
	if got != want {
		t.Fatalf("SupportFor(my-plugin) = %+v, want %+v", got, want)
	}
}

func TestSupportForReturnsTheRow(t *testing.T) {
	if got := SupportFor("ecs-fargate"); !got.Canary || got.Weighting != WeightingTargetGroups {
		t.Fatalf("SupportFor(ecs-fargate) = %+v", got)
	}
}

func TestEveryNoteIsPlainProse(t *testing.T) {
	for _, row := range append(Matrix(), SupportFor("my-plugin")) {
		if strings.Contains(row.Note, "\u2014") {
			t.Errorf("%s: note has an em dash", row.Provider)
		}
		if !strings.HasSuffix(row.Note, ".") {
			t.Errorf("%s: note %q should end with a full stop", row.Provider, row.Note)
		}
	}
}

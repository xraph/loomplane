//go:build integration

package integration

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hallproof/witness"
	witnessforge "github.com/hallproof/witness/forge"
	"github.com/hallproof/witness/ingesttest"
	"github.com/hallproof/witness/push"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/config"
)

// coverageWait bounds the wait for witness's coverage snapshot. Witness
// spreads each collector's first run over its first 30 seconds, so the
// snapshot lands anywhere in that window, plus a push.
const coverageWait = 60 * time.Second

// witnessCoverage is the witness.coverage snapshot's settings.
type witnessCoverage struct {
	Scope    string `json:"scope"`
	Adapters []struct {
		Name  string `json:"name"`
		Found bool   `json:"found"`
		Error bool   `json:"error"`
	} `json:"adapters"`
}

// With hallproof.endpoint set, Loomplane runs witness, and witness finds the
// libraries Loomplane runs. Bastion and Sentinel aren't imported, so they
// can't be found. Takes up to 30 seconds after boot, for the first run,
// and then the app has to stop in time.
func TestWitnessFindsLoomplanesLibraries(t *testing.T) {
	s := ingesttest.New(t)
	s.Know(push.KindHeartbeat)

	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Dashboard.Dir = t.TempDir()
	cfg.Hallproof = config.HallproofConfig{Endpoint: s.URL, IngestKey: s.Key}
	booted := time.Now()
	r := start(t, cfg)
	defer r.stop()

	cov := waitForCoverage(t, s)
	t.Logf("coverage arrived %s after boot", time.Since(booted).Round(time.Second))
	if cov.Scope != "staff" {
		t.Errorf("scope %q, want staff", cov.Scope)
	}
	found := map[string]bool{}
	for _, a := range cov.Adapters {
		t.Logf("adapter %-10s found=%v error=%v", a.Name, a.Found, a.Error)
		found[a.Name] = a.Found
		if a.Error {
			t.Errorf("adapter %s failed to attach; the app log says why", a.Name)
		}
	}
	for _, name := range []string{"chronicle", "authsome", "warden", "keysmith", "vault", "trove", "grove", "relay", "dispatch"} {
		if !found[name] {
			t.Errorf("witness didn't find %s", name)
		}
	}
	// Bastion and Sentinel aren't run.
	for _, name := range []string{"bastion", "sentinel"} {
		if found[name] {
			t.Errorf("witness attached %s", name)
		}
	}

	// Witness is in the container once the app has started.
	if _, err := forge.InjectType[*witness.Witness](r.app.(forge.App).Container()); err != nil {
		t.Errorf("*witness.Witness isn't provided after start: %v", err)
	}

	// Every message pushed so far was accepted: one Hallproof refuses
	// would be lost evidence.
	stored := map[string]bool{}
	keys := map[string]bool{}
	for _, m := range s.Stored() {
		stored[m.ID] = true
		k, _, _ := strings.Cut(m.ID, ":")
		keys[k] = true
	}
	for _, req := range s.Requests() {
		for _, m := range req.Batch.Messages {
			if m.Kind != push.KindHeartbeat && !stored[m.ID] {
				t.Errorf("a %s message %s was refused (status %d)", m.Kind, m.ID, req.Status)
			}
		}
	}
	got := make([]string, 0, len(keys))
	for k := range keys {
		got = append(got, k)
	}
	slices.Sort(got)
	t.Logf("evidence pushed so far: %v", got)
}

// With no endpoint, witness isn't mounted, so nothing of it starts.
func TestWitnessOffWithoutEndpoint(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Dashboard.Dir = t.TempDir()
	r := start(t, cfg)
	defer r.stop()

	app := r.app.(forge.App)
	if _, err := app.GetExtension(witnessforge.Name); err == nil {
		t.Error("witness is registered with no hallproof.endpoint")
	}
	if _, err := forge.InjectType[*witness.Witness](app.Container()); err == nil {
		t.Error("*witness.Witness is provided with no hallproof.endpoint")
	}
}

// waitForCoverage waits, at most coverageWait, for the coverage snapshot.
func waitForCoverage(t *testing.T, s *ingesttest.Server) witnessCoverage {
	t.Helper()
	deadline := time.Now().Add(coverageWait)
	for time.Now().Before(deadline) {
		for _, m := range s.Stored() {
			if !strings.HasPrefix(m.ID, witnessforge.CoverageKey+":") {
				continue
			}
			body, err := push.DecodeBody[push.ConfigSnapshot](m)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(body.Settings)
			if err != nil {
				t.Fatal(err)
			}
			var cov witnessCoverage
			if err := json.Unmarshal(raw, &cov); err != nil {
				t.Fatal(err)
			}
			return cov
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no %s snapshot within %s", witnessforge.CoverageKey, coverageWait)
	return witnessCoverage{}
}

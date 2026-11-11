package currentauthority

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/grove/driver"
	keyid "github.com/xraph/keysmith/id"

	"github.com/xraph/loomplane/internal/security"
)

func fleetTestBinding() FleetBinding {
	b := testBinding()
	b.Actor.KeyID = keyid.NewKeyID().String()
	b.Actor.UserID = security.FleetSubject(b.Actor.KeyID)
	b.Actor.Kind = security.KindFleet
	b.Actor.Fleet = &security.FleetScope{ProjectID: "project", FleetID: "fleet"}
	return FleetBinding(b)
}

func TestFleetCanonicalCopiesScopeAndRejectsOtherKinds(t *testing.T) {
	b := fleetTestBinding()
	original, err := canonicalFleet(b)
	if err != nil {
		t.Fatal(err)
	}
	b.Actor.Fleet.FleetID = "changed"
	if original.fleet != "fleet" {
		t.Fatal("retained caller scope")
	}
	if actor(original).Kind != security.KindFleet || actor(original).Fleet.FleetID != "fleet" {
		t.Fatal("lost service identity")
	}
	for name, change := range map[string]func(*FleetBinding){
		"person":   func(b *FleetBinding) { b.Actor.Kind = security.KindUser },
		"readonly": func(b *FleetBinding) { b.Actor.ReadOnly = true },
		"subject":  func(b *FleetBinding) { b.Actor.UserID = "user" },
		"key": func(b *FleetBinding) {
			b.Actor.KeyID = "invalid"
			b.Actor.UserID = security.FleetSubject(b.Actor.KeyID)
		},
		"scope":      func(b *FleetBinding) { b.Actor.Fleet = nil },
		"project":    func(b *FleetBinding) { b.Actor.Fleet.ProjectID = "" },
		"fleet":      func(b *FleetBinding) { b.Actor.Fleet.FleetID = "" },
		"delegation": func(b *FleetBinding) { b.Actor.OnBehalfOf = &security.Principal{} },
		"digest":     func(b *FleetBinding) { b.InputDigest = "bad" },
	} {
		t.Run(name, func(t *testing.T) {
			b := fleetTestBinding()
			change(&b)
			if _, err := canonicalFleet(b); !errors.Is(err, ErrBinding) {
				t.Fatalf("accepted %v", err)
			}
		})
	}
	if _, err := canonical(Binding(fleetTestBinding())); !errors.Is(err, ErrBinding) {
		t.Fatal("ordinary path accepted fleet")
	}
}

type refusedFleetBackend struct{ calls int }

func (b *refusedFleetBackend) begin(context.Context) (nativeTx, error) {
	b.calls++
	return nil, ErrUnavailable
}
func (*refusedFleetBackend) close() error { return nil }

func TestFleetProfileEntryGatesBeforeAcquisition(t *testing.T) {
	for _, tc := range []struct {
		name            string
		p               *profile
		ordinary, fleet bool
	}{
		{name: "nil"}, {name: "zero", p: &profile{}},
		{name: "fixture", p: &profile{mode: mechanismProfile}, ordinary: true},
		{name: "ordinary", p: &profile{mode: installedProfile, ordinary: &ordinaryProfile{}}, ordinary: true},
		{name: "fleet", p: &profile{mode: installedProfile, fleet: &fleetProfile{appID: testBinding().AppID}}, fleet: true},
		{name: "both", p: &profile{mode: installedProfile, ordinary: &ordinaryProfile{}, fleet: &fleetProfile{appID: testBinding().AppID}}, ordinary: true, fleet: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &refusedFleetBackend{}
			c := &Coordinator{backend: backend, schema: "public", profile: tc.p, source: func(context.Context, driver.Tx, Binding) error { return nil }}
			work := func(context.Context, *Attempt) error { t.Fatal("callback ran"); return nil }
			_ = c.Run(t.Context(), testBinding(), work)
			if (backend.calls == 1) != tc.ordinary {
				t.Fatalf("ordinary acquired=%d", backend.calls)
			}
			backend.calls = 0
			_ = c.RunFleet(t.Context(), fleetTestBinding(), work)
			if (backend.calls == 1) != tc.fleet {
				t.Fatalf("fleet acquired=%d", backend.calls)
			}
		})
	}
}

func TestFleetFactsAtExactInstant(t *testing.T) {
	b, _ := canonicalFleet(fleetTestBinding())
	at := time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC)
	base := fleetFacts{key: b.key, app: b.app, org: b.org, orgApp: b.app, creator: "audit-creator", state: "active", access: "write", kind: string(security.KindFleet), project: b.project, fleet: b.fleet, until: optionalInstant{present: true, value: at.Add(time.Nanosecond)}}
	if err := base.validate(b, at); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*fleetFacts){
		"expired":          func(f *fleetFacts) { f.until.value = at },
		"missing expiry":   func(f *fleetFacts) { f.until.present = false },
		"revoked":          func(f *fleetFacts) { f.revoked = true },
		"scopes":           func(f *fleetFacts) { f.scopes = true },
		"policy":           func(f *fleetFacts) { f.policy = true },
		"creator":          func(f *fleetFacts) { f.creator = "" },
		"organization":     func(f *fleetFacts) { f.org = "other" },
		"organization app": func(f *fleetFacts) { f.orgApp = "other" },
		"project":          func(f *fleetFacts) { f.project = "other" },
		"fleet":            func(f *fleetFacts) { f.fleet = "other" },
		"kind":             func(f *fleetFacts) { f.kind = "user" },
		"access":           func(f *fleetFacts) { f.access = "read" },
		"state":            func(f *fleetFacts) { f.state = "disabled" },
	} {
		t.Run(name, func(t *testing.T) {
			f := base
			change(&f)
			if err := f.validate(b, at); !errors.Is(err, security.ErrForbidden) {
				t.Fatalf("accepted %v", err)
			}
		})
	}
}

func TestFleetRunRejectsAppBeforeAcquisition(t *testing.T) {
	b := fleetTestBinding()
	for _, installedApp := range []string{"", "different-app", b.AppID} {
		t.Run("installed-"+installedApp, func(t *testing.T) {
			backend := &refusedFleetBackend{}
			c := &Coordinator{backend: backend, schema: "qualified", profile: &profile{mode: installedProfile, fleet: &fleetProfile{appID: installedApp}}, source: func(context.Context, driver.Tx, Binding) error { return nil }}
			err := c.RunFleet(t.Context(), b, func(context.Context, *Attempt) error {
				t.Fatal("historical callback entered for mismatched application")
				return nil
			})
			if installedApp != b.AppID {
				if !errors.Is(err, ErrBinding) || backend.calls != 0 {
					t.Fatalf("mismatched application err=%v acquired=%d", err, backend.calls)
				}
			} else if !errors.Is(err, ErrUnavailable) || backend.calls != 1 {
				t.Fatalf("matching application err=%v acquired=%d", err, backend.calls)
			}
		})
	}
}

package currentauthority

import (
	"context"
	"time"

	"github.com/xraph/grove/driver"
	keyid "github.com/xraph/keysmith/id"

	"github.com/xraph/loomplane/internal/security"
)

// FleetBinding names one operation performed by a scoped Fleet service key.
// Its input digest includes the domain grant and the expected durable revision.
type FleetBinding Binding

type fleetProfile struct{ appID string }

type fleetFacts struct {
	key, app, org, orgApp, creator, state, access, kind, project, fleet string
	revoked, policy, scopes                                             bool
	until                                                               optionalInstant
}

type fleetProof struct{ facts fleetFacts }

func canonicalFleet(input FleetBinding) (binding, error) {
	b := Binding(input)
	p := b.Actor
	if p.Kind != security.KindFleet || p.ReadOnly || p.OnBehalfOf != nil || p.Fleet == nil || p.OrgID == "" || p.Fleet.ProjectID == "" || p.Fleet.FleetID == "" || p.UserID != security.FleetSubject(p.KeyID) {
		return binding{}, ErrBinding
	}
	key, err := keyid.ParseKeyID(p.KeyID)
	if err != nil || key.String() != p.KeyID {
		return binding{}, ErrBinding
	}
	bound, err := canonicalOperation(b)
	if err != nil {
		return binding{}, err
	}
	bound.project = p.Fleet.ProjectID
	bound.fleet = p.Fleet.FleetID
	return bound, nil
}

func publicBinding(b binding) Binding {
	return Binding{Actor: actor(b), AppID: b.app, Operation: b.operation, InputDigest: b.digest, RecordID: b.record, AttemptID: b.attempt}
}

// RunFleet owns the same bounded transaction lifecycle as Run. Results become
// publishable only after it returns a known successful commit.
func (c *Coordinator) RunFleet(ctx context.Context, b FleetBinding, work func(context.Context, *Attempt) error) error {
	if c == nil {
		return ErrUnavailable
	}
	bound, err := canonicalFleet(b)
	if err != nil {
		return err
	}
	return c.run(ctx, bound, work)
}

// Schema returns the verified schema captured by this attempt.
func (a *Attempt) Schema() string { return a.schema }

func (f fleetFacts) validate(b binding, at time.Time) error {
	if b.kind != security.KindFleet || at.IsZero() || f.key != b.key || f.app != b.app || f.org != b.org || f.orgApp != b.app || f.creator == "" || f.state != "active" || f.access != "write" || f.kind != string(security.KindFleet) || f.project != b.project || f.fleet != b.fleet || f.revoked || f.policy || f.scopes || !f.until.present || !f.until.value.After(at) {
		return security.ErrForbidden
	}
	return nil
}

// EvaluateFleet reads the current organization and exact persisted service key.
// The domain adapter separately locks and validates its grant and instance facts.
func (a *Attempt) EvaluateFleet(ctx context.Context) (*Decision, error) {
	if a.binding.kind != security.KindFleet {
		return nil, a.fail(ErrBinding)
	}
	if a.profile == nil || a.profile.mode != installedProfile || a.profile.fleet == nil {
		return nil, a.fail(ErrUnavailable)
	}
	if a.binding.app != a.profile.fleet.appID {
		return nil, a.fail(ErrBinding)
	}
	proof := &fleetProof{}
	return a.evaluate(ctx, func(ctx context.Context) error {
		facts, err := a.readFleetFacts(ctx)
		if err != nil {
			return err
		}
		proof.facts = facts
		return nil
	}, nil, proof)
}

// StageFleet consumes the key decision once. The domain callback validates its
// locked facts at the supplied instant and writes only through the owned handle.
func (a *Attempt) StageFleet(ctx context.Context, d *Decision, durable FleetBinding, write func(context.Context, driver.Tx, time.Time) error) error {
	bound, err := canonicalFleet(durable)
	if err != nil {
		return a.fail(err)
	}
	return a.stage(ctx, d, bound, write)
}

func (d *Decision) validateFleet(a *Attempt) error {
	if a.profile == nil || a.profile.mode != installedProfile || a.profile.fleet == nil || a.binding.kind != security.KindFleet || a.binding.app != a.profile.fleet.appID || d.kind != fleetDecision || d.fleet == nil || d.ordinary != nil {
		return ErrDecision
	}
	return d.fleet.facts.validate(a.binding, d.at)
}

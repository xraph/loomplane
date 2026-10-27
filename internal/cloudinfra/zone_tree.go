package cloudinfra

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// A zone's parent is the nearest zone of the same organization whose name
// the zone's name sits strictly under, matched on whole labels. It is worked
// out from the organization's zones on every read and never stored, so
// adding or removing a domain regroups its subdomains on its own.
//
// A subdomain is verified through its domain: proving _loomplane.<domain>
// in public DNS proves control of every name under it. A zone that wasn't
// linked reads its own proof only, so a caller that skips the linking
// fails closed.

// LinkZoneParents sets the parent of every zone in zones to its nearest
// ancestor in the same slice and organization. Links from an earlier call
// are cleared first.
func LinkZoneParents(zones []*Zone) {
	for _, z := range zones {
		z.parent = nil
	}
	for _, z := range zones {
		z.parent = nearestAncestor(z.Name, z.OrgID, zones)
	}
}

// nearestAncestor is the zone of org with the longest name that name sits
// strictly under, or nil.
func nearestAncestor(name, org string, zones []*Zone) *Zone {
	for _, ancestor := range ancestorNames(name) {
		for _, z := range zones {
			if z.OrgID == org && z.Name == ancestor {
				return z
			}
		}
	}
	return nil
}

// ancestorNames lists the names name sits strictly under, longest first.
// A bare top-level label is never one: "x.app.loomplane.com" gives
// "app.loomplane.com" and "loomplane.com".
func ancestorNames(name string) []string {
	out := []string{}
	for {
		_, rest, ok := strings.Cut(name, ".")
		if !ok || !strings.Contains(rest, ".") {
			return out
		}
		out = append(out, rest)
		name = rest
	}
}

// subdomainsOf lists every zone of zone's organization whose name sits
// strictly under zone's, at any depth, sorted by name.
func subdomainsOf(zone *Zone, zones []*Zone) []*Zone {
	out := []*Zone{}
	for _, z := range zones {
		if z.OrgID == zone.OrgID && strings.HasSuffix(z.Name, "."+zone.Name) {
			out = append(out, z)
		}
	}
	slices.SortFunc(out, func(a, b *Zone) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// ownVerified is a zone checked against its own TXT proof.
func (z *Zone) ownVerified() bool {
	return !z.VerifiedAt.IsZero() && z.hasControlProof()
}

// proofZone is the zone whose TXT proof decides this one: its own once
// verified, else the nearest verified ancestor's, else whichever proof is
// still pending, its own first. It is never nil.
func (z *Zone) proofZone() *Zone {
	if z.ownVerified() {
		return z
	}
	var inherited *Zone
	if z.parent != nil {
		inherited = z.parent.proofZone()
		if inherited.ownVerified() {
			return inherited
		}
	}
	if inherited == nil || z.hasControlProof() {
		return z
	}
	return inherited
}

// verifiedAncestor is the nearest ancestor checked against its own TXT
// proof, or nil.
func (z *Zone) verifiedAncestor() *Zone {
	for a := z.parent; a != nil; a = a.parent {
		if a.ownVerified() {
			return a
		}
	}
	return nil
}

// verified is a zone a project can route through: checked against its own
// TXT proof, or against the proof of a domain it sits under.
func (z *Zone) verified() bool {
	return z.proofZone().ownVerified()
}

// Verified reports verified() to other packages.
func (z *Zone) Verified() bool {
	return z.verified()
}

// VerifiedProjectZones lists the zones granted to project that it may name
// hosts in. The whole list is linked first, because a subdomain's domain
// need not be granted to the project. A zone with only a legacy VerifiedAt
// still counts here, as it always has.
func VerifiedProjectZones(zones []*Zone, project string) []ProjectZone {
	LinkZoneParents(zones)
	out := []ProjectZone{}
	for _, z := range zones {
		if slices.Contains(z.ProjectIDs, project) && (z.Verified() || !z.VerifiedAt.IsZero()) {
			out = append(out, ProjectZone{ID: z.ID, Name: z.Name, Mode: z.Mode, Verified: true})
		}
	}
	return out
}

// linkedZone loads the organization's zones, links them, and returns the
// one with id.
func (s *Service) linkedZone(ctx context.Context, st DomainStore, org, id string) (*Zone, error) {
	zone, _, err := s.linkedZones(ctx, st, org, id)
	return zone, err
}

// linkedZones is linkedZone with every zone of the organization it read.
func (s *Service) linkedZones(ctx context.Context, st DomainStore, org, id string) (zone *Zone, zones []*Zone, err error) {
	if zones, err = st.Zones(ctx, org); err != nil {
		return nil, nil, err
	}
	LinkZoneParents(zones)
	for _, z := range zones {
		if z.ID == id {
			return z, zones, nil
		}
	}
	return nil, nil, ErrNotFound
}

// deeperZoneNames lists the names strictly under zoneName that hostname's
// base is or sits under, longest first. The base drops a wildcard's *.
func deeperZoneNames(zoneName, hostname string) []string {
	base := strings.TrimPrefix(hostname, "*.")
	out := []string{}
	for _, name := range append([]string{base}, ancestorNames(base)...) {
		if strings.HasSuffix(name, "."+zoneName) {
			out = append(out, name)
		}
	}
	return out
}

// deeperZone is the deepest zone of zone's organization, strictly under
// zone, that hostname's base is or sits under, or nil. A route for that
// name belongs there, since the subdomain has its own grants.
func deeperZone(zone *Zone, hostname string, zones []*Zone) *Zone {
	for _, name := range deeperZoneNames(zone.Name, hostname) {
		for _, z := range zones {
			if z.OrgID == zone.OrgID && z.Name == name {
				return z
			}
		}
	}
	return nil
}

// errBelongsTo refuses a new route on a zone for a name a subdomain holds.
func errBelongsTo(hostname, subdomain string) error {
	return fmt.Errorf("%w: %s belongs to %s. Add it there", ErrInvalid, hostname, subdomain)
}

// checkDeepestZone refuses hostname on zone when a subdomain holds it.
// Routes that predate the subdomain aren't checked here and stay as they
// are.
func checkDeepestZone(zone *Zone, hostname string, zones []*Zone) error {
	if sub := deeperZone(zone, hostname, zones); sub != nil {
		return errBelongsTo(hostname, sub.Name)
	}
	return nil
}
